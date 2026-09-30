package materializedview

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/schemahelpers"
)

//go:embed materializedview.md
var materializedViewResourceDescription string

var (
	_ resource.Resource                     = &Resource{}
	_ resource.ResourceWithConfigure        = &Resource{}
	_ resource.ResourceWithConfigValidators = &Resource{}
	_ resource.ResourceWithValidateConfig   = &Resource{}
	_ resource.ResourceWithModifyPlan       = &Resource{}
	_ resource.ResourceWithImportState      = &Resource{}
)

func NewResource() resource.Resource {
	return &Resource{}
}

type Resource struct {
	client dbops.Client
}

func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_materialized_view"
}

func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := schemahelpers.CommonSchemaAttributes("materialized view")
	attrs["columns"] = schemahelpers.ColumnsAttribute("Optional inline materialized-view columns for engine-backed definitions.")
	attrs["engine"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw ClickHouse engine expression. Set this or to_table, but not both.",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs["partition_by"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw PARTITION BY clause expression for engine-backed materialized views",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs["order_by"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw ORDER BY clause expression for engine-backed materialized views",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs["primary_key"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw PRIMARY KEY clause expression for engine-backed materialized views",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs["sample_by"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw SAMPLE BY clause expression for engine-backed materialized views",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs["ttl"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw TTL clause expression for engine-backed materialized views",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs["settings"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw SETTINGS clause body for engine-backed materialized views",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs["populate"] = schema.BoolAttribute{
		Optional:    true,
		Description: "Whether to append POPULATE to the CREATE MATERIALIZED VIEW statement",
		PlanModifiers: []planmodifier.Bool{
			boolplanmodifier.RequiresReplace(),
		},
	}
	attrs["to_table"] = schema.StringAttribute{
		Optional:    true,
		Description: "Destination table for TO-based materialized views. Usually this references clickhousedbops_table.<name>.qualified_name.",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs["to_columns"] = schemahelpers.ColumnSignaturesAttribute("Optional destination signature appended after TO <table> (...). Only name, type, and nullable are supported there. When omitted, the column list that ClickHouse infers is not tracked.")
	attrs["query"] = schema.StringAttribute{
		Required:    true,
		Description: "Raw SELECT query used by the materialized view definition. With to_table a change is applied in place with ALTER TABLE ... MODIFY QUERY. With engine a change replaces the materialized view.",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	resp.Schema = schema.Schema{
		Attributes:          attrs,
		MarkdownDescription: materializedViewResourceDescription,
	}
}

func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	r.client = req.ProviderData.(dbops.Client)
}

func (r *Resource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(path.MatchRoot("engine"), path.MatchRoot("to_table")),
	}
}

func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	if req.Config.Raw.IsNull() {
		return
	}

	var config MaterializedViewResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !config.Populate.IsNull() && !config.Populate.IsUnknown() &&
		!config.ToTable.IsNull() && !config.ToTable.IsUnknown() &&
		config.Populate.ValueBool() {
		resp.Diagnostics.AddAttributeError(
			path.Root("populate"),
			"Invalid Attribute Combination",
			"'populate' can only be set for engine-backed materialized views and cannot be combined with 'to_table'.",
		)
	}
	if !config.ToTable.IsNull() && !config.ToTable.IsUnknown() && !config.Columns.IsNull() && !config.Columns.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("columns"),
			"Invalid Attribute Combination",
			"'columns' can only be set for engine-backed materialized views. Use 'to_columns' with 'to_table' instead.",
		)
	}
	if !config.Engine.IsNull() && !config.Engine.IsUnknown() && !config.ToColumns.IsNull() && !config.ToColumns.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("to_columns"),
			"Invalid Attribute Combination",
			"'to_columns' can only be set with 'to_table' and cannot be combined with 'engine'.",
		)
	}
	if !config.Engine.IsNull() && !config.Engine.IsUnknown() && (config.OrderBy.IsNull() || config.OrderBy.IsUnknown() || config.OrderBy.ValueString() == "") {
		resp.Diagnostics.AddAttributeError(
			path.Root("order_by"),
			"Missing Required Attribute for Engine-Backed Materialized View",
			"'order_by' must be set when 'engine' is used.",
		)
	}
	if !config.ToTable.IsNull() && !config.ToTable.IsUnknown() {
		for _, attr := range []struct {
			path  path.Path
			value types.String
		}{
			{path.Root("partition_by"), config.PartitionBy},
			{path.Root("order_by"), config.OrderBy},
			{path.Root("primary_key"), config.PrimaryKey},
			{path.Root("sample_by"), config.SampleBy},
			{path.Root("ttl"), config.TTL},
			{path.Root("settings"), config.Settings},
		} {
			if !attr.value.IsNull() && !attr.value.IsUnknown() {
				resp.Diagnostics.AddAttributeError(
					attr.path,
					"Invalid Attribute Combination",
					"Engine-backed table clauses cannot be combined with 'to_table'.",
				)
			}
		}
	}
}

func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}

	var plan MaterializedViewResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(schemahelpers.PlanNodes(ctx, r.client, plan.ClusterName, &resp.Plan)...)
	if resp.Diagnostics.HasError() || req.State.Raw.IsNull() {
		return
	}

	// MODIFY QUERY works only for a materialized view that writes to a separate table, and
	// the provider runs it per node, not ON CLUSTER.
	var state MaterializedViewResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if (plan.ToTable.IsNull() || !plan.ClusterName.IsNull()) && !schemahelpers.SQLEqual(plan.Query.ValueString(), state.Query.ValueString()) {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("query"))
	}
	if resp.Diagnostics.HasError() || !plan.Columns.Equal(state.Columns) || !plan.ToColumns.Equal(state.ToColumns) || !plan.Populate.Equal(state.Populate) {
		return
	}
	resp.Diagnostics.Append(schemahelpers.KeepEquivalentStrings(ctx, &resp.Plan, state.Nodes, state.CreateStatement, []schemahelpers.EquivalentString{
		{Attribute: "query", Planned: plan.Query, State: state.Query},
		{Attribute: "to_table", Planned: plan.ToTable, State: state.ToTable},
		{Attribute: "engine", Planned: plan.Engine, State: state.Engine},
		{Attribute: "partition_by", Planned: plan.PartitionBy, State: state.PartitionBy},
		{Attribute: "order_by", Planned: plan.OrderBy, State: state.OrderBy},
		{Attribute: "primary_key", Planned: plan.PrimaryKey, State: state.PrimaryKey},
		{Attribute: "sample_by", Planned: plan.SampleBy, State: state.SampleBy},
		{Attribute: "ttl", Planned: plan.TTL, State: state.TTL},
		{Attribute: "settings", Planned: plan.Settings, State: state.Settings},
	})...)
}

func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan MaterializedViewResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	state, diags := r.convergeMaterializedView(ctx, plan, r.client.AdoptExisting())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MaterializedViewResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, view, nodes, diags := schemahelpers.ReadNodes(ctx, r.client, state,
		func(ctx context.Context, client dbops.Client) (*dbops.MaterializedView, error) {
			return client.GetMaterializedView(ctx, state.Database.ValueString(), state.Name.ValueString(), state.ClusterName.ValueStringPointer())
		},
		syncMaterializedViewState,
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
	var plan MaterializedViewResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	state, diags := r.convergeMaterializedView(ctx, plan, true)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MaterializedViewResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(schemahelpers.DeleteNodes(ctx, r.client, func(ctx context.Context, client dbops.Client) error {
		return client.DeleteMaterializedView(ctx, state.Database.ValueString(), state.Name.ValueString(), state.ClusterName.ValueStringPointer())
	})...)
}

// convergeMaterializedView makes every node hold the planned materialized view. Only the
// query of a TO-table materialized view can change in place.
func (r *Resource) convergeMaterializedView(ctx context.Context, plan MaterializedViewResourceModel, adopt bool) (*MaterializedViewResourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	desired, err := expandMaterializedViewModel(ctx, plan)
	diags.Append(schemahelpers.DiagnosticsFromErr("Invalid materialized view configuration", err)...)
	if diags.HasError() {
		return nil, diags
	}

	clusterName := plan.ClusterName.ValueStringPointer()
	view, nodes, convergeDiags := schemahelpers.ConvergeNodes(ctx, r.client, adopt, schemahelpers.NodeConverger[dbops.MaterializedView]{
		Kind:          "materialized view",
		QualifiedName: schemahelpers.QualifiedName(desired.Database, desired.Name),
		Get: func(ctx context.Context, client dbops.Client) (*dbops.MaterializedView, error) {
			return client.GetMaterializedView(ctx, desired.Database, desired.Name, clusterName)
		},
		Create: func(ctx context.Context, client dbops.Client, _ *dbops.MaterializedView) error {
			_, err := client.CreateMaterializedView(ctx, desired, clusterName)
			return err
		},
		Reconcile: func(ctx context.Context, node dbops.SchemaNode, _ bool, existing *dbops.MaterializedView) error {
			candidate := plan
			if syncDiags := syncMaterializedViewState(ctx, &candidate, existing); syncDiags.HasError() {
				return schemahelpers.DiagnosticsError(syncDiags)
			}

			queryChanged := !candidate.Query.Equal(plan.Query)
			replaceAttrs := make([]string, 0)
			for name, values := range map[string][2]attr.Value{
				"columns":      {candidate.Columns, plan.Columns},
				"engine":       {candidate.Engine, plan.Engine},
				"partition_by": {candidate.PartitionBy, plan.PartitionBy},
				"order_by":     {candidate.OrderBy, plan.OrderBy},
				"primary_key":  {candidate.PrimaryKey, plan.PrimaryKey},
				"sample_by":    {candidate.SampleBy, plan.SampleBy},
				"ttl":          {candidate.TTL, plan.TTL},
				"settings":     {candidate.Settings, plan.Settings},
				"to_table":     {candidate.ToTable, plan.ToTable},
				"to_columns":   {candidate.ToColumns, plan.ToColumns},
			} {
				if !values[0].Equal(values[1]) {
					replaceAttrs = append(replaceAttrs, name)
				}
			}
			if queryChanged && desired.ToTable == "" {
				replaceAttrs = append(replaceAttrs, "query")
			}
			if len(replaceAttrs) > 0 {
				slices.Sort(replaceAttrs)
				return fmt.Errorf("the materialized view differs from the desired definition in attributes that cannot be changed in place: %s. Replace the materialized view or change the configuration to match it",
					strings.Join(replaceAttrs, ", "))
			}
			if !queryChanged {
				return nil
			}
			if clusterName != nil {
				return errors.New("the query of a materialized view cannot be changed in place with cluster_name; use the provider fanout_cluster instead")
			}
			return node.Client.ModifyMaterializedViewQuery(ctx, desired.Database, desired.Name, desired.Query)
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

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	schemahelpers.ImportSchemaObjectState(ctx, req, resp)
}

func syncMaterializedViewState(ctx context.Context, state *MaterializedViewResourceModel, view *dbops.MaterializedView) diag.Diagnostics {
	var diags diag.Diagnostics

	state.Query = schemahelpers.SyncEquivalentString(state.Query, view.Query)
	state.Engine = schemahelpers.SyncEquivalentString(state.Engine, view.Engine)
	state.PartitionBy = schemahelpers.SyncEquivalentString(state.PartitionBy, view.PartitionBy)
	state.OrderBy = schemahelpers.SyncEquivalentString(state.OrderBy, view.OrderBy)
	state.PrimaryKey = schemahelpers.SyncEquivalentString(state.PrimaryKey, view.PrimaryKey)
	state.SampleBy = schemahelpers.SyncEquivalentString(state.SampleBy, view.SampleBy)
	state.TTL = schemahelpers.SyncEquivalentString(state.TTL, view.TTL)
	state.Settings = schemahelpers.SyncEquivalentString(state.Settings, view.Settings)
	state.ToTable = schemahelpers.SyncEquivalentString(state.ToTable, view.ToTable)

	if view.Populate || (!state.Populate.IsNull() && !state.Populate.IsUnknown()) {
		state.Populate = types.BoolValue(view.Populate)
	}

	// Sync columns for engine-backed materialized views. ClickHouse stores an inferred column
	// list, which is compared only when the configuration sets columns.
	currentColumns, columnDiags := schemahelpers.ExpandColumns(ctx, state.Columns)
	diags.Append(columnDiags...)
	if diags.HasError() {
		return diags
	}
	if !state.Columns.IsNull() && !schemahelpers.ColumnsEqual(currentColumns, view.Columns) {
		columns, columnDiags := schemahelpers.ColumnsValue(ctx, view.Columns)
		diags.Append(columnDiags...)
		if diags.HasError() {
			return diags
		}
		state.Columns = columns
	}

	// ClickHouse stores an inferred column list for every TO-table materialized view. It is
	// compared only when the configuration sets to_columns.
	if state.ToColumns.IsNull() {
		return diags
	}
	currentToColumns, toColumnDiags := schemahelpers.ExpandColumnSignatures(ctx, state.ToColumns)
	diags.Append(toColumnDiags...)
	if diags.HasError() {
		return diags
	}
	if !schemahelpers.ColumnSignaturesEqual(currentToColumns, view.ToColumns) {
		toColumns, columnDiags := schemahelpers.ColumnSignaturesValue(ctx, view.ToColumns)
		diags.Append(columnDiags...)
		if diags.HasError() {
			return diags
		}
		state.ToColumns = toColumns
	}

	return diags
}

func expandMaterializedViewModel(ctx context.Context, plan MaterializedViewResourceModel) (dbops.MaterializedView, error) {
	columns, diags := schemahelpers.ExpandColumns(ctx, plan.Columns)
	if diags.HasError() {
		return dbops.MaterializedView{}, schemahelpers.DiagnosticsError(diags)
	}

	toColumns, diags := schemahelpers.ExpandColumnSignatures(ctx, plan.ToColumns)
	if diags.HasError() {
		return dbops.MaterializedView{}, schemahelpers.DiagnosticsError(diags)
	}

	populate := false
	if !plan.Populate.IsNull() {
		populate = plan.Populate.ValueBool()
	}

	return dbops.MaterializedView{
		Database:    plan.Database.ValueString(),
		Name:        plan.Name.ValueString(),
		Columns:     columns,
		Engine:      plan.Engine.ValueString(),
		PartitionBy: plan.PartitionBy.ValueString(),
		OrderBy:     plan.OrderBy.ValueString(),
		PrimaryKey:  plan.PrimaryKey.ValueString(),
		SampleBy:    plan.SampleBy.ValueString(),
		TTL:         plan.TTL.ValueString(),
		Settings:    plan.Settings.ValueString(),
		Populate:    populate,
		ToTable:     plan.ToTable.ValueString(),
		ToColumns:   toColumns,
		Query:       plan.Query.ValueString(),
	}, nil
}
