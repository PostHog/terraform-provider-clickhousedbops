package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
)

type sqlPlanServer struct {
	tfprotov6.ProviderServer
	provider *Provider
}

type savedSQLPlan struct {
	Version     int                             `json:"sql_plan_version"`
	Private     []byte                          `json:"private,omitempty"`
	Operations  []clickhouseclient.SQLOperation `json:"operations"`
	Deletes     []clickhouseclient.SQLOperation `json:"deletes,omitempty"`
	DeleteState *tfprotov6.DynamicValue         `json:"delete_state,omitempty"`
}

// Protocol6 preserves the reviewed writes in PlannedPrivate, including destroy plans
// whose planned state is null. The digest in planned state binds apply-time re-planning.
func Protocol6() tfprotov6.ProviderServer {
	p := &Provider{}
	return &sqlPlanServer{ProviderServer: providerserver.NewProtocol6(p)(), provider: p}
}

func sqlDiagnostic(err error) *tfprotov6.Diagnostic {
	return &tfprotov6.Diagnostic{Severity: tfprotov6.DiagnosticSeverityError, Summary: "Cannot enforce SQL execution plan", Detail: err.Error()}
}

func hasErrors(diagnostics []*tfprotov6.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == tfprotov6.DiagnosticSeverityError {
			return true
		}
	}
	return false
}

func decodeSQLPlan(data []byte) (savedSQLPlan, bool) {
	var saved savedSQLPlan
	err := json.Unmarshal(data, &saved)
	return saved, err == nil && saved.Version == 1
}

func setSQLPlanDigest(value *tfprotov6.DynamicValue, typ tftypes.Type, digest string) (*tfprotov6.DynamicValue, error) {
	raw, err := value.Unmarshal(typ)
	if err != nil || raw.IsNull() {
		return value, err
	}
	var attributes map[string]tftypes.Value
	if err := raw.As(&attributes); err != nil {
		return nil, err
	}
	attributes["sql_plan_digest"] = tftypes.NewValue(tftypes.String, digest)
	result, err := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, attributes))
	return &result, err
}

func priorSQLPlanDigest(value tftypes.Value) (string, error) {
	if value.IsNull() {
		return "", nil
	}
	var attributes map[string]tftypes.Value
	if err := value.As(&attributes); err != nil {
		return "", err
	}
	var digest string
	if attribute := attributes["sql_plan_digest"]; attribute.IsKnown() && !attribute.IsNull() {
		if err := attribute.As(&digest); err != nil {
			return "", err
		}
	}
	return digest, nil
}

func sqlReport(operations []clickhouseclient.SQLOperation) (string, error) {
	report := make([]clickhouseclient.SQLOperation, len(operations))
	for index, operation := range operations {
		report[index] = operation
		report[index].SQL = operation.DisplaySQL
		report[index].DisplaySQL = ""
		if len(operation.Parameters) > 0 {
			report[index].Parameters = make(map[string]string, len(operation.Parameters))
			for key := range operation.Parameters {
				report[index].Parameters[key] = "[REDACTED]"
			}
		}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	return string(data), err
}

func (s *sqlPlanServer) PlanResourceChange(ctx context.Context, req *tfprotov6.PlanResourceChangeRequest) (*tfprotov6.PlanResourceChangeResponse, error) {
	request := *req
	previous, previousSaved := decodeSQLPlan(req.PriorPrivate)
	if previousSaved {
		request.PriorPrivate = previous.Private
	}
	resp, err := s.ProviderServer.PlanResourceChange(ctx, &request)
	if err != nil || hasErrors(resp.Diagnostics) || resp.Deferred != nil {
		return resp, err
	}
	schemas, err := s.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		return nil, err
	}
	typ := schemas.ResourceSchemas[req.TypeName].ValueType()
	objectType := typ.(tftypes.Object)
	_, supported := objectType.AttributeTypes["sql_plan_digest"]
	if !supported {
		if s.provider.enforceSQLPlan {
			prior, priorErr := req.PriorState.Unmarshal(typ)
			planned, plannedErr := resp.PlannedState.Unmarshal(typ)
			if priorErr != nil || plannedErr != nil || !planned.Equal(prior) {
				resp.Diagnostics = append(resp.Diagnostics, sqlDiagnostic(fmt.Errorf("%s does not support enforce_sql_plan; use a separate provider configuration for this resource", req.TypeName)))
			}
		}
		return resp, nil
	}
	prior, err := req.PriorState.Unmarshal(typ)
	if err != nil {
		resp.Diagnostics = append(resp.Diagnostics, sqlDiagnostic(err))
		return resp, nil
	}
	digest, err := priorSQLPlanDigest(prior)
	if err != nil {
		resp.Diagnostics = append(resp.Diagnostics, sqlDiagnostic(err))
		return resp, nil
	}
	if !s.provider.enforceSQLPlan {
		resp.PlannedState, err = setSQLPlanDigest(resp.PlannedState, typ, digest)
		if err != nil {
			resp.Diagnostics = append(resp.Diagnostics, sqlDiagnostic(err))
		}
		return resp, nil
	}
	config, err := req.Config.Unmarshal(typ)
	if err != nil || !config.IsFullyKnown() {
		resp.Diagnostics = append(resp.Diagnostics, sqlDiagnostic(fmt.Errorf("all configuration values must be known before SQL can be reviewed")))
		return resp, nil
	}
	planned, err := resp.PlannedState.Unmarshal(typ)
	if err != nil {
		resp.Diagnostics = append(resp.Diagnostics, sqlDiagnostic(err))
		return resp, nil
	}
	saved := savedSQLPlan{Version: 1, Private: resp.PlannedPrivate}
	previewCtx, recording := clickhouseclient.RecordSQL(ctx)
	preview := tfprotov6.ApplyResourceChangeRequest{TypeName: req.TypeName, PriorState: req.PriorState, PlannedState: resp.PlannedState, Config: req.Config, PlannedPrivate: resp.PlannedPrivate, ProviderMeta: req.ProviderMeta}
	// Core re-plans replacements with a null prior state. Preserve the reviewed drop
	// and simulate it again so the create half sees the object as absent.
	if !planned.IsNull() && (len(resp.RequiresReplace) > 0 || (previousSaved && previous.DeleteState != nil && prior.IsNull())) {
		deleteState := req.PriorState
		if prior.IsNull() {
			deleteState = previous.DeleteState
		}
		null, nullErr := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, nil))
		if nullErr != nil {
			resp.Diagnostics = append(resp.Diagnostics, sqlDiagnostic(nullErr))
			return resp, nil
		}
		deletion := preview
		deletion.PriorState, deletion.PlannedState = deleteState, &null
		deleteResp, deleteErr := s.ProviderServer.ApplyResourceChange(previewCtx, &deletion)
		if deleteErr != nil {
			return nil, deleteErr
		}
		if hasErrors(deleteResp.Diagnostics) {
			resp.Diagnostics = append(resp.Diagnostics, deleteResp.Diagnostics...)
			return resp, nil
		}
		saved.Deletes = append(saved.Deletes, recording.Operations...)
		saved.DeleteState = deleteState
		recording.Operations = nil
		preview.PriorState = &null
	}
	candidate, err := setSQLPlanDigest(resp.PlannedState, typ, digest)
	if err != nil {
		resp.Diagnostics = append(resp.Diagnostics, sqlDiagnostic(err))
		return resp, nil
	}
	candidateValue, err := candidate.Unmarshal(typ)
	if err != nil {
		resp.Diagnostics = append(resp.Diagnostics, sqlDiagnostic(err))
		return resp, nil
	}
	if candidateValue.Equal(prior) {
		resp.PlannedState = candidate
		resp.PlannedPrivate, err = json.Marshal(saved)
		return resp, err
	}
	previewResp, err := s.ProviderServer.ApplyResourceChange(previewCtx, &preview)
	if err != nil {
		return nil, err
	}
	if hasErrors(previewResp.Diagnostics) {
		resp.Diagnostics = append(resp.Diagnostics, previewResp.Diagnostics...)
		return resp, nil
	}
	saved.Operations = recording.Operations
	all := append(append([]clickhouseclient.SQLOperation{}, saved.Deletes...), saved.Operations...)
	if len(saved.Operations) > 0 {
		digest, err = clickhouseclient.SQLPlanDigest(saved.Operations)
		if err != nil {
			resp.Diagnostics = append(resp.Diagnostics, sqlDiagnostic(err))
			return resp, nil
		}
	}
	if len(all) > 0 {
		display, reportErr := sqlReport(all)
		if reportErr != nil {
			resp.Diagnostics = append(resp.Diagnostics, sqlDiagnostic(reportErr))
			return resp, nil
		}
		resp.Diagnostics = append(resp.Diagnostics, &tfprotov6.Diagnostic{Severity: tfprotov6.DiagnosticSeverityWarning, Summary: "Reviewed SQL execution plan", Detail: display})
	}

	resp.PlannedPrivate, err = json.Marshal(saved)
	if err == nil {
		resp.PlannedState, err = setSQLPlanDigest(resp.PlannedState, typ, digest)
	}
	if err != nil {
		resp.Diagnostics = append(resp.Diagnostics, sqlDiagnostic(err))
	}
	return resp, nil
}

func (s *sqlPlanServer) ApplyResourceChange(ctx context.Context, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	if !s.provider.enforceSQLPlan {
		return s.ProviderServer.ApplyResourceChange(ctx, req)
	}
	saved, ok := decodeSQLPlan(req.PlannedPrivate)
	if !ok {
		return &tfprotov6.ApplyResourceChangeResponse{NewState: req.PriorState, Diagnostics: []*tfprotov6.Diagnostic{sqlDiagnostic(fmt.Errorf("saved SQL execution plan is missing; create a new plan"))}}, nil
	}
	schemas, err := s.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		return nil, err
	}
	planned, err := req.PlannedState.Unmarshal(schemas.ResourceSchemas[req.TypeName].ValueType())
	if err != nil {
		return nil, err
	}
	if !planned.IsNull() {
		digest, err := priorSQLPlanDigest(planned)
		if len(saved.Operations) > 0 {
			expected, digestErr := clickhouseclient.SQLPlanDigest(saved.Operations)
			if err != nil || digestErr != nil || digest != expected {
				return &tfprotov6.ApplyResourceChangeResponse{NewState: req.PriorState, Diagnostics: []*tfprotov6.Diagnostic{sqlDiagnostic(fmt.Errorf("SQL manifest does not match the saved plan digest; create a new plan"))}}, nil
			}
		}
	}
	operations := saved.Operations
	if planned.IsNull() && saved.DeleteState != nil {
		operations = saved.Deletes
	}
	request := *req
	request.PlannedPrivate = saved.Private
	return s.ProviderServer.ApplyResourceChange(clickhouseclient.EnforceSQL(ctx, operations), &request)
}
