package view

import (
	"context"
	_ "embed"
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

	var clusterName types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("cluster_name"), &clusterName)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(schemahelpers.PlanNodes(ctx, r.client, clusterName, &resp.Plan)...)
}

func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
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
	var state ViewResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, view, nodes, diags := schemahelpers.ReadNodes(ctx, r.client, state,
		func(ctx context.Context, client dbops.Client) (*dbops.View, error) {
			return client.GetView(ctx, state.Database.ValueString(), state.Name.ValueString(), state.ClusterName.ValueStringPointer())
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

	newState.Nodes = nodes
	schemahelpers.SyncObjectState(newState.ClusterName, newState.Database, newState.Name, view.CreateStatement, &newState.ID, &newState.QualifiedName, &newState.CreateStatement)
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
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
	var state ViewResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(schemahelpers.DeleteNodes(ctx, r.client, func(ctx context.Context, client dbops.Client) error {
		return client.DeleteView(ctx, state.Database.ValueString(), state.Name.ValueString(), state.ClusterName.ValueStringPointer())
	})...)
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	schemahelpers.ImportSchemaObjectState(ctx, req, resp)
}

// convergeView makes every node hold the planned view. A view that differs is replaced in
// place with CREATE OR REPLACE VIEW.
func (r *Resource) convergeView(ctx context.Context, plan ViewResourceModel, adopt bool) (*ViewResourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	desired, err := expandViewModel(ctx, plan)
	diags.Append(schemahelpers.DiagnosticsFromErr("Invalid view configuration", err)...)
	if diags.HasError() {
		return nil, diags
	}

	clusterName := plan.ClusterName.ValueStringPointer()
	view, nodes, convergeDiags := schemahelpers.ConvergeNodes(ctx, r.client, adopt, schemahelpers.NodeConverger[dbops.View]{
		Kind:          "view",
		QualifiedName: schemahelpers.QualifiedName(desired.Database, desired.Name),
		Get: func(ctx context.Context, client dbops.Client) (*dbops.View, error) {
			return client.GetView(ctx, desired.Database, desired.Name, clusterName)
		},
		Create: func(ctx context.Context, client dbops.Client, _ *dbops.View) error {
			_, err := client.CreateView(ctx, desired, clusterName)
			return err
		},
		Reconcile: func(ctx context.Context, node dbops.SchemaNode, _ bool, existing *dbops.View) error {
			candidate := plan
			if syncDiags := syncViewState(ctx, &candidate, existing); syncDiags.HasError() {
				return schemahelpers.DiagnosticsError(syncDiags)
			}
			if reflect.DeepEqual(candidate, plan) {
				return nil
			}
			_, err := node.Client.ReplaceView(ctx, desired, clusterName)
			return err
		},
	})
	diags.Append(convergeDiags...)
	if diags.HasError() {
		return nil, diags
	}

	state := plan
	state.Nodes = nodes
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
