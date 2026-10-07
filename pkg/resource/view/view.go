package view

import (
	"context"
	_ "embed"
	"fmt"
	"reflect"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/schemahelpers"
)

//go:embed view.md
var viewResourceDescription string

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithModifyPlan  = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

func NewResource() resource.Resource {
	return &Resource{}
}

type Resource struct {
	client dbops.Client
}

func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_view"
}

func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := schemahelpers.CommonSchemaAttributes("view")
	attrs["columns"] = schemahelpers.ColumnSignaturesAttributeWithPlanModifiers("Optional view signature. Only name, type, and nullable are included in the CREATE VIEW signature. When omitted, the column list that ClickHouse infers is not tracked.", nil)
	attrs["query"] = schema.StringAttribute{
		Required:    true,
		Description: "Raw SELECT query used by the view definition. A change is applied in place with CREATE OR REPLACE VIEW.",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	resp.Schema = schema.Schema{
		Attributes:          attrs,
		MarkdownDescription: viewResourceDescription,
	}
}

func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	r.client = req.ProviderData.(dbops.Client)
}

func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}

	client, nodeDiags := schemahelpers.NodeClient(ctx, r.client, req.Plan.GetAttribute)
	resp.Diagnostics.Append(nodeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r = &Resource{client: client}
	if req.State.Raw.IsNull() {
		return
	}

	var plan, state ViewResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, converged, diags := schemahelpers.PlanObject(ctx, plan,
		func(ctx context.Context) (*dbops.View, error) {
			return r.client.GetView(ctx, plan.Database.ValueString(), plan.Name.ValueString(), plan.ClusterName.ValueStringPointer())
		}, syncViewState)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !converged {
		state.CreateStatement = types.StringUnknown()
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("create_statement"), types.StringUnknown())...)
	}
	if !plan.Columns.Equal(state.Columns) {
		return
	}
	resp.Diagnostics.Append(schemahelpers.KeepEquivalentStrings(ctx, &resp.Plan, state.CreateStatement, []schemahelpers.EquivalentString{
		{Attribute: "query", Planned: plan.Query, State: state.Query},
	})...)
}

func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	client, nodeDiags := schemahelpers.NodeClient(ctx, r.client, req.Plan.GetAttribute)
	resp.Diagnostics.Append(nodeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r = &Resource{client: client}
	var plan ViewResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	state, diags := r.convergeView(ctx, plan, r.client.AdoptExisting())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	client, nodeDiags := schemahelpers.NodeClient(ctx, r.client, req.State.GetAttribute)
	resp.Diagnostics.Append(nodeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r = &Resource{client: client}
	var state ViewResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, view, diags := schemahelpers.ReadObject(ctx, state,
		func(ctx context.Context) (*dbops.View, error) {
			return r.client.GetView(ctx, state.Database.ValueString(), state.Name.ValueString(), state.ClusterName.ValueStringPointer())
		},
		syncViewState,
	)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if newState == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	schemahelpers.SyncObjectState(newState.ClusterName, newState.Database, newState.Name, view.CreateStatement, &newState.ID, &newState.QualifiedName, &newState.CreateStatement)
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	client, nodeDiags := schemahelpers.NodeClient(ctx, r.client, req.Plan.GetAttribute)
	resp.Diagnostics.Append(nodeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r = &Resource{client: client}
	var plan ViewResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	state, diags := r.convergeView(ctx, plan, true)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	client, nodeDiags := schemahelpers.NodeClient(ctx, r.client, req.State.GetAttribute)
	resp.Diagnostics.Append(nodeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r = &Resource{client: client}
	var state ViewResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteView(ctx, state.Database.ValueString(), state.Name.ValueString(), state.ClusterName.ValueStringPointer()); err != nil {
		resp.Diagnostics.AddError("Error deleting view", fmt.Sprintf("%+v\n", err))
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	schemahelpers.ImportSchemaObjectState(ctx, req, resp)
}

// convergeView makes the node hold the planned view. A view that differs is replaced in
// place with CREATE OR REPLACE VIEW.
func (r *Resource) convergeView(ctx context.Context, plan ViewResourceModel, adopt bool) (*ViewResourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	desired, err := expandViewModel(ctx, plan)
	diags.Append(schemahelpers.DiagnosticsFromErr("Invalid view configuration", err)...)
	if diags.HasError() {
		return nil, diags
	}

	clusterName := plan.ClusterName.ValueStringPointer()
	view, convergeDiags := schemahelpers.Converge(ctx, adopt, schemahelpers.Converger[dbops.View]{
		Kind:          "view",
		QualifiedName: schemahelpers.QualifiedName(desired.Database, desired.Name),
		Get: func(ctx context.Context) (*dbops.View, error) {
			return r.client.GetView(ctx, desired.Database, desired.Name, clusterName)
		},
		Create: func(ctx context.Context) error {
			_, err := r.client.CreateView(ctx, desired, clusterName)
			return err
		},
		Reconcile: func(ctx context.Context, existing *dbops.View) error {
			candidate := plan
			if syncDiags := syncViewState(ctx, &candidate, existing); syncDiags.HasError() {
				return schemahelpers.DiagnosticsError(syncDiags)
			}
			if reflect.DeepEqual(candidate, plan) {
				return nil
			}
			_, err := r.client.ReplaceView(ctx, desired, clusterName)
			return err
		},
	})
	diags.Append(convergeDiags...)
	if diags.HasError() {
		return nil, diags
	}

	state := plan
	schemahelpers.SyncObjectState(state.ClusterName, state.Database, state.Name, view.CreateStatement, &state.ID, &state.QualifiedName, &state.CreateStatement)

	return &state, diags
}

func expandViewModel(ctx context.Context, plan ViewResourceModel) (dbops.View, error) {
	columns, diags := schemahelpers.ExpandColumnSignatures(ctx, plan.Columns)
	if diags.HasError() {
		return dbops.View{}, schemahelpers.DiagnosticsError(diags)
	}

	return dbops.View{
		Database: plan.Database.ValueString(),
		Name:     plan.Name.ValueString(),
		Columns:  columns,
		Query:    plan.Query.ValueString(),
	}, nil
}

func syncViewState(ctx context.Context, state *ViewResourceModel, view *dbops.View) diag.Diagnostics {
	var diags diag.Diagnostics

	state.Query = schemahelpers.SyncEquivalentString(state.Query, view.Query)

	// ClickHouse stores an inferred column list for every view. It is compared only when
	// the configuration sets columns.
	if state.Columns.IsNull() {
		return diags
	}
	currentColumns, columnDiags := schemahelpers.ExpandColumnSignatures(ctx, state.Columns)
	diags.Append(columnDiags...)
	if diags.HasError() {
		return diags
	}
	if !schemahelpers.ColumnSignaturesEqual(currentColumns, view.Columns) {
		columns, columnDiags := schemahelpers.ColumnSignaturesValue(ctx, view.Columns)
		diags.Append(columnDiags...)
		if diags.HasError() {
			return diags
		}
		state.Columns = columns
	}

	return diags
}
