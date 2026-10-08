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

const (
	attributeEngine      = "engine"
	attributePartitionBy = "partition_by"
	attributeOrderBy     = "order_by"
	attributePrimaryKey  = "primary_key"
	attributeSampleBy    = "sample_by"
	attributeTtl         = "ttl"
	attributeSettings    = "settings"
	attributePopulate    = "populate"
	attributeToTable     = "to_table"
	attributeColumns     = "columns"
	attributeToColumns   = "to_columns"
)

type Resource struct {
	client dbops.Client
}

func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_materialized_view"
}

func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := schemahelpers.CommonSchemaAttributes("materialized view")
	attrs[attributeColumns] = schemahelpers.ColumnsAttribute("Optional inline materialized-view columns for engine-backed definitions.")
	attrs[attributeEngine] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw ClickHouse engine expression. Set this or to_table, but not both.",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs[attributePartitionBy] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw PARTITION BY clause expression for engine-backed materialized views",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs[attributeOrderBy] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw ORDER BY clause expression for engine-backed materialized views",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs[attributePrimaryKey] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw PRIMARY KEY clause expression for engine-backed materialized views",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs[attributeSampleBy] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw SAMPLE BY clause expression for engine-backed materialized views",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs[attributeTtl] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw TTL clause expression for engine-backed materialized views",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs[attributeSettings] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw SETTINGS clause body for engine-backed materialized views",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs[attributePopulate] = schema.BoolAttribute{
		Optional:    true,
		Description: "Whether to append POPULATE to the CREATE MATERIALIZED VIEW statement",
		PlanModifiers: []planmodifier.Bool{
			boolplanmodifier.RequiresReplace(),
		},
	}
	attrs[attributeToTable] = schema.StringAttribute{
		Optional:    true,
		Description: "Destination table for TO-based materialized views. Usually this references clickhousedbops_table.<name>.qualified_name.",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	attrs[attributeToColumns] = schemahelpers.ColumnSignaturesAttribute("Optional destination signature appended after TO <table> (...). Only name, type, and nullable are supported there. When omitted, the column list that ClickHouse infers is not tracked.")
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
		resourcevalidator.ExactlyOneOf(path.MatchRoot(attributeEngine), path.MatchRoot(attributeToTable)),
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
			path.Root(attributePopulate),
			"Invalid Attribute Combination",
			"'populate' can only be set for engine-backed materialized views and cannot be combined with 'to_table'.",
		)
	}
	if !config.ToTable.IsNull() && !config.ToTable.IsUnknown() && !config.Columns.IsNull() && !config.Columns.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root(attributeColumns),
			"Invalid Attribute Combination",
			"'columns' can only be set for engine-backed materialized views. Use 'to_columns' with 'to_table' instead.",
		)
	}
	if !config.Engine.IsNull() && !config.Engine.IsUnknown() && !config.ToColumns.IsNull() && !config.ToColumns.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root(attributeToColumns),
			"Invalid Attribute Combination",
			"'to_columns' can only be set with 'to_table' and cannot be combined with 'engine'.",
		)
	}
	if !config.Engine.IsNull() && !config.Engine.IsUnknown() && (config.OrderBy.IsNull() || config.OrderBy.IsUnknown() || config.OrderBy.ValueString() == "") {
		resp.Diagnostics.AddAttributeError(
			path.Root(attributeOrderBy),
			"Missing Required Attribute for Engine-Backed Materialized View",
			"'order_by' must be set when 'engine' is used.",
		)
	}
	if !config.ToTable.IsNull() && !config.ToTable.IsUnknown() {
		for _, attr := range []struct {
			path  path.Path
			value types.String
		}{
			{path.Root(attributePartitionBy), config.PartitionBy},
			{path.Root(attributeOrderBy), config.OrderBy},
			{path.Root(attributePrimaryKey), config.PrimaryKey},
			{path.Root(attributeSampleBy), config.SampleBy},
			{path.Root(attributeTtl), config.TTL},
			{path.Root(attributeSettings), config.Settings},
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

	client, nodeDiags := schemahelpers.NodeClient(ctx, r.client, req.Plan.GetAttribute)
	resp.Diagnostics.Append(nodeDiags...)
	if resp.Diagnostics.HasError() || req.State.Raw.IsNull() {
		return
	}
	r = &Resource{client: client}

	// MODIFY QUERY works only for a materialized view that writes to a separate table, and
	// the provider runs it on the node, not ON CLUSTER.
	var state MaterializedViewResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	remote, converged, diags := schemahelpers.PlanObject(ctx, plan,
		func(ctx context.Context) (*dbops.MaterializedView, error) {
			return r.client.GetMaterializedView(ctx, plan.Database.ValueString(), plan.Name.ValueString(), plan.ClusterName.ValueStringPointer())
		}, syncMaterializedViewState)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !converged {
		state.CreateStatement = types.StringUnknown()
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("create_statement"), types.StringUnknown())...)
	}
	if remote != nil {
		resp.RequiresReplace = append(resp.RequiresReplace, nodeReplacementPaths(plan, *remote)...)
	}
	if (plan.ToTable.IsNull() || !plan.ClusterName.IsNull()) && !schemahelpers.SQLEqual(plan.Query.ValueString(), state.Query.ValueString()) {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("query"))
	}
	if len(resp.RequiresReplace) > 0 {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("create_statement"))
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("create_statement"), types.StringUnknown())...)
		state.CreateStatement = types.StringUnknown()
	}
	if resp.Diagnostics.HasError() || !plan.Columns.Equal(state.Columns) || !plan.ToColumns.Equal(state.ToColumns) || !plan.Populate.Equal(state.Populate) {
		return
	}
	resp.Diagnostics.Append(schemahelpers.KeepEquivalentStrings(ctx, &resp.Plan, state.CreateStatement, []schemahelpers.EquivalentString{
		{Attribute: "query", Planned: plan.Query, State: state.Query},
		{Attribute: attributeToTable, Planned: plan.ToTable, State: state.ToTable},
		{Attribute: attributeEngine, Planned: plan.Engine, State: state.Engine},
		{Attribute: attributePartitionBy, Planned: plan.PartitionBy, State: state.PartitionBy},
		{Attribute: attributeOrderBy, Planned: plan.OrderBy, State: state.OrderBy},
		{Attribute: attributePrimaryKey, Planned: plan.PrimaryKey, State: state.PrimaryKey},
		{Attribute: attributeSampleBy, Planned: plan.SampleBy, State: state.SampleBy},
		{Attribute: attributeTtl, Planned: plan.TTL, State: state.TTL},
		{Attribute: attributeSettings, Planned: plan.Settings, State: state.Settings},
	})...)
}

func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	client, nodeDiags := schemahelpers.NodeClient(ctx, r.client, req.Plan.GetAttribute)
	resp.Diagnostics.Append(nodeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r = &Resource{client: client}
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
	client, nodeDiags := schemahelpers.NodeClient(ctx, r.client, req.State.GetAttribute)
	resp.Diagnostics.Append(nodeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r = &Resource{client: client}
	var state MaterializedViewResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, view, diags := schemahelpers.ReadObject(ctx, state,
		func(ctx context.Context) (*dbops.MaterializedView, error) {
			return r.client.GetMaterializedView(ctx, state.Database.ValueString(), state.Name.ValueString(), state.ClusterName.ValueStringPointer())
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
	client, nodeDiags := schemahelpers.NodeClient(ctx, r.client, req.State.GetAttribute)
	resp.Diagnostics.Append(nodeDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	r = &Resource{client: client}
	var state MaterializedViewResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteMaterializedView(ctx, state.Database.ValueString(), state.Name.ValueString(), state.ClusterName.ValueStringPointer()); err != nil {
		resp.Diagnostics.AddError("Error deleting object", fmt.Sprintf("%+v\n", err))
	}
}

// convergeMaterializedView makes the node hold the planned materialized view. Only the
// query of a TO-table materialized view can change in place.
func (r *Resource) convergeMaterializedView(ctx context.Context, plan MaterializedViewResourceModel, adopt bool) (*MaterializedViewResourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	desired, err := expandMaterializedViewModel(ctx, plan)
	diags.Append(schemahelpers.DiagnosticsFromErr("Invalid materialized view configuration", err)...)
	if diags.HasError() {
		return nil, diags
	}

	clusterName := plan.ClusterName.ValueStringPointer()
	view, convergeDiags := schemahelpers.Converge(ctx, adopt, schemahelpers.Converger[dbops.MaterializedView]{
		Kind:          "materialized view",
		QualifiedName: schemahelpers.QualifiedName(desired.Database, desired.Name),
		Get: func(ctx context.Context) (*dbops.MaterializedView, error) {
			return r.client.GetMaterializedView(ctx, desired.Database, desired.Name, clusterName)
		},
		Create: func(ctx context.Context) error {
			_, err := r.client.CreateMaterializedView(ctx, desired, clusterName)
			return err
		},
		Reconcile: func(ctx context.Context, existing *dbops.MaterializedView) error {
			candidate := plan
			if syncDiags := syncMaterializedViewState(ctx, &candidate, existing); syncDiags.HasError() {
				return schemahelpers.DiagnosticsError(syncDiags)
			}

			queryChanged := !candidate.Query.Equal(plan.Query)
			replaceAttrs := make([]string, 0)
			for name, values := range map[string][2]attr.Value{
				attributeColumns:     {candidate.Columns, plan.Columns},
				attributeEngine:      {candidate.Engine, plan.Engine},
				attributePartitionBy: {candidate.PartitionBy, plan.PartitionBy},
				attributeOrderBy:     {candidate.OrderBy, plan.OrderBy},
				attributePrimaryKey:  {candidate.PrimaryKey, plan.PrimaryKey},
				attributeSampleBy:    {candidate.SampleBy, plan.SampleBy},
				attributeTtl:         {candidate.TTL, plan.TTL},
				attributeSettings:    {candidate.Settings, plan.Settings},
				attributeToTable:     {candidate.ToTable, plan.ToTable},
				attributeToColumns:   {candidate.ToColumns, plan.ToColumns},
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
				return errors.New("the query of a materialized view cannot be changed in place with cluster_name; declare one resource per node instead")
			}
			return r.client.ModifyMaterializedViewQuery(ctx, desired.Database, desired.Name, desired.Query)
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
