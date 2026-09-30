package table

import (
	"context"
	_ "embed"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/tableengine"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/schemahelpers"
)

//go:embed table.md
var tableResourceDescription string

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
	resp.TypeName = req.ProviderTypeName + "_table"
}

func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := schemahelpers.CommonSchemaAttributes("table")
	attrs["engine"] = schema.StringAttribute{
		Required:    true,
		Description: "Raw ClickHouse engine expression, for example MergeTree(), Distributed(...), or Kafka(...)",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplaceIf(
				func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
					resp.RequiresReplace = !enginesEquivalent(req.StateValue.ValueString(), req.PlanValue.ValueString())
				},
				"Replaces the table when the engine changes, ignoring formatting.",
				"Replaces the table when the engine changes, ignoring formatting.",
			),
		},
	}
	attrs["columns"] = schemahelpers.ColumnsAttributeWithPlanModifiers("Structured column definitions. This can be assigned directly from a local list of objects.", nil)
	maps.Copy(attrs, elementSchemaAttributes())
	attrs["partition_by"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw PARTITION BY clause expression",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attrs["order_by"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw ORDER BY clause expression",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attrs["primary_key"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw PRIMARY KEY clause expression",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attrs["sample_by"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw SAMPLE BY clause expression",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attrs["ttl"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw TTL clause expression",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attrs["settings"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw SETTINGS clause body",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attrs["as_select"] = schema.StringAttribute{
		Optional:    true,
		Description: "Optional raw query appended as AS <query> after the table definition",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
	}
	resp.Schema = schema.Schema{
		Attributes:          attrs,
		MarkdownDescription: tableResourceDescription,
	}
}

func (r *Resource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	r.client = req.ProviderData.(dbops.Client)
}

func (r *Resource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	if r.client == nil {
		return
	}

	var plan TableResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(schemahelpers.PlanNodes(ctx, r.client, plan.ClusterName, &resp.Plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	desiredTable, err := expandTableModel(ctx, plan)
	resp.Diagnostics.Append(schemahelpers.DiagnosticsFromErr("Invalid table configuration", err)...)
	if resp.Diagnostics.HasError() {
		return
	}

	columnPatterns, indexPatterns, err := unmanagedPatterns(ctx, plan)
	resp.Diagnostics.Append(schemahelpers.DiagnosticsFromErr("Invalid table configuration", err)...)
	if resp.Diagnostics.HasError() {
		return
	}

	capabilities, err := r.client.GetTableEngineCapabilities(ctx, desiredTable.Engine)
	if err != nil {
		resp.Diagnostics.AddError("Error reading table engine capabilities", fmt.Sprintf("%+v\n", err))
		return
	}

	resp.Diagnostics.Append(validateTableForEngine(desiredTable, capabilities)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if req.State.Raw.IsNull() {
		return
	}

	var state TableResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	currentTable, err := expandTableModel(ctx, state)
	resp.Diagnostics.Append(schemahelpers.DiagnosticsFromErr("Invalid table configuration", err)...)
	if resp.Diagnostics.HasError() {
		return
	}
	currentTable = filterUnmanaged(currentTable, desiredTable, columnPatterns, indexPatterns)
	if r.client.IgnoreColumnOrder() {
		currentTable.Columns = alignColumnOrder(currentTable.Columns, desiredTable.Columns)
	}

	settingNames := collectSettingNames(currentTable.Settings, desiredTable.Settings)
	settingCapabilities, err := r.client.GetTableSettingCapabilities(ctx, currentTable.Engine, settingNames)
	if err != nil {
		resp.Diagnostics.AddError("Error reading table setting capabilities", fmt.Sprintf("%+v\n", err))
		return
	}

	// An attribute whose configured text means the same as the text in state keeps the state's
	// text, so that formatting, column order and default settings do not show as changes.
	keep := func(attr string, equivalent bool, value attr.Value, apply func()) {
		if !equivalent {
			return
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(attr), value)...)
		apply()
	}
	keep("engine", enginesEquivalent(currentTable.Engine, desiredTable.Engine), state.Engine, func() { desiredTable.Engine = currentTable.Engine })
	keep("partition_by", expressionsEqual(currentTable.PartitionBy, desiredTable.PartitionBy), state.PartitionBy, func() { desiredTable.PartitionBy = currentTable.PartitionBy })
	keep("order_by", expressionListsEqual(currentTable.OrderBy, desiredTable.OrderBy), state.OrderBy, func() { desiredTable.OrderBy = currentTable.OrderBy })
	keep("primary_key", expressionListsEqual(currentTable.PrimaryKey, desiredTable.PrimaryKey), state.PrimaryKey, func() { desiredTable.PrimaryKey = currentTable.PrimaryKey })
	keep("sample_by", expressionsEqual(currentTable.SampleBy, desiredTable.SampleBy), state.SampleBy, func() { desiredTable.SampleBy = currentTable.SampleBy })
	keep("ttl", ttlExpressionsEqual(currentTable.TTL, desiredTable.TTL), state.TTL, func() { desiredTable.TTL = currentTable.TTL })
	keep("as_select", expressionsEqual(currentTable.AsSelect, desiredTable.AsSelect), state.AsSelect, func() { desiredTable.AsSelect = currentTable.AsSelect })
	if syncRemoteSettings(state.Settings, desiredTable.Settings, settingCapabilities).Equal(state.Settings) {
		// Terraform does not accept a planned null for an attribute the configuration sets.
		if !state.Settings.IsNull() || plan.Settings.IsNull() {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("settings"), state.Settings)...)
		}
		desiredTable.Settings = currentTable.Settings
	}
	keep("columns", schemahelpers.ColumnsEqual(currentTable.Columns, desiredTable.Columns), state.Columns, func() { desiredTable.Columns = currentTable.Columns })
	keep("indexes", namedListsEqual(currentTable.Indexes, desiredTable.Indexes, indexName, indexEqual), state.Indexes, func() { desiredTable.Indexes = currentTable.Indexes })
	keep("projections", namedListsEqual(currentTable.Projections, desiredTable.Projections, projectionName, projectionEqual), state.Projections, func() { desiredTable.Projections = currentTable.Projections })
	keep("constraints", namedListsEqual(currentTable.Constraints, desiredTable.Constraints, constraintName, constraintEqual), state.Constraints, func() { desiredTable.Constraints = currentTable.Constraints })
	if resp.Diagnostics.HasError() {
		return
	}

	updatePlan, err := planTableUpdate(currentTable, desiredTable, capabilities, settingCapabilities)
	if err != nil {
		resp.Diagnostics.AddError("Error planning table update", fmt.Sprintf("%+v\n", err))
		return
	}

	for attr := range updatePlan.ReplaceAttrs {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root(attr))
	}

	// Nothing to run on any node: the create statement stays what it is.
	var plannedNodes types.List
	resp.Diagnostics.Append(resp.Plan.GetAttribute(ctx, path.Root("nodes"), &plannedNodes)...)
	if len(updatePlan.ReplaceAttrs) == 0 && len(updatePlan.ActionGroups) == 0 && plannedNodes.Equal(state.Nodes) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("create_statement"), state.CreateStatement)...)
	}
}

func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan TableResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	state, diags := r.convergeTable(ctx, plan, r.client.AdoptExisting())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state TableResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	declared, err := expandTableModel(ctx, state)
	resp.Diagnostics.Append(schemahelpers.DiagnosticsFromErr("Invalid table state", err)...)
	columnPatterns, indexPatterns, err := unmanagedPatterns(ctx, state)
	resp.Diagnostics.Append(schemahelpers.DiagnosticsFromErr("Invalid table state", err)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, table, nodes, diags := schemahelpers.ReadNodes(ctx, r.client, state,
		func(ctx context.Context, client dbops.Client) (*dbops.Table, error) {
			table, err := client.GetTable(ctx, state.Database.ValueString(), state.Name.ValueString(), state.ClusterName.ValueStringPointer())
			if err != nil || table == nil {
				return nil, err
			}
			filtered := filterUnmanaged(*table, declared, columnPatterns, indexPatterns)
			if r.client.IgnoreColumnOrder() {
				filtered.Columns = alignColumnOrder(filtered.Columns, declared.Columns)
			}
			return &filtered, nil
		},
		func(ctx context.Context, state *TableResourceModel, table *dbops.Table) diag.Diagnostics {
			var diags diag.Diagnostics
			var settingCapabilities map[string]dbops.TableSettingCapability
			if normalizeSQL(table.Settings) != "" {
				var err error
				settingCapabilities, err = r.client.GetTableSettingCapabilities(ctx, table.Engine, collectSettingNames(table.Settings))
				if err != nil {
					diags.AddWarning(
						"Error reading table setting capabilities",
						fmt.Sprintf("Skipping remote settings reconciliation because setting capabilities could not be read: %+v", err),
					)
				}
			}
			diags.Append(syncTableState(ctx, state, table, settingCapabilities)...)
			return diags
		},
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
	schemahelpers.SyncObjectState(newState.ClusterName, newState.Database, newState.Name, table.CreateStatement, &newState.ID, &newState.QualifiedName, &newState.CreateStatement)
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan TableResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	state, diags := r.convergeTable(ctx, plan, true)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state TableResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(schemahelpers.DeleteNodes(ctx, r.client, func(ctx context.Context, client dbops.Client) error {
		return client.DeleteTable(ctx, state.Database.ValueString(), state.Name.ValueString(), state.ClusterName.ValueStringPointer())
	})...)
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	schemahelpers.ImportSchemaObjectState(ctx, req, resp)
}

// convergeTable makes every node hold the planned table: it alters existing tables in place
// and creates the table where it is missing.
func (r *Resource) convergeTable(ctx context.Context, plan TableResourceModel, adopt bool) (*TableResourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	desired, err := expandTableModel(ctx, plan)
	diags.Append(schemahelpers.DiagnosticsFromErr("Invalid table configuration", err)...)
	columnPatterns, indexPatterns, err := unmanagedPatterns(ctx, plan)
	diags.Append(schemahelpers.DiagnosticsFromErr("Invalid table configuration", err)...)
	if diags.HasError() {
		return nil, diags
	}

	capabilities, err := r.client.GetTableEngineCapabilities(ctx, desired.Engine)
	if err != nil {
		diags.AddError("Error reading table engine capabilities", fmt.Sprintf("%+v\n", err))
		return nil, diags
	}

	clusterName := plan.ClusterName.ValueStringPointer()
	// ClickHouse replicates every ALTER of a Replicated table through Keeper, except MODIFY
	// SETTING and RESET SETTING, which change only the replica that runs them.
	replicated := strings.HasPrefix(strings.ToLower(tableengine.BaseName(desired.Engine)), "replicated")

	table, nodes, convergeDiags := schemahelpers.ConvergeNodes(ctx, r.client, adopt, schemahelpers.NodeConverger[dbops.Table]{
		Kind:          "table",
		QualifiedName: schemahelpers.QualifiedName(desired.Database, desired.Name),
		Get: func(ctx context.Context, client dbops.Client) (*dbops.Table, error) {
			return client.GetTable(ctx, desired.Database, desired.Name, clusterName)
		},
		Create: func(ctx context.Context, client dbops.Client, reference *dbops.Table) error {
			table := desired
			if replicated && reference != nil {
				// A new replica must declare the structure that Keeper holds for the shard:
				// the same indexes in the same order, and the unmanaged columns and indexes.
				table.Columns, table.Indexes, table.Projections, table.Constraints = reference.Columns, reference.Indexes, reference.Projections, reference.Constraints
			}
			_, err := client.CreateTable(ctx, table, clusterName)
			return err
		},
		Reconcile: func(ctx context.Context, node dbops.SchemaNode, firstInShard bool, existing *dbops.Table) error {
			remote := filterUnmanaged(*existing, desired, columnPatterns, indexPatterns)
			if r.client.IgnoreColumnOrder() {
				remote.Columns = alignColumnOrder(remote.Columns, desired.Columns)
			}
			settingCapabilities, err := r.client.GetTableSettingCapabilities(ctx, remote.Engine, collectSettingNames(remote.Settings, desired.Settings))
			if err != nil {
				return err
			}

			// The node's table is read the way Read reads it, so that text that is only
			// formatted differently from the plan is not a change.
			candidate := plan
			if syncDiags := syncTableState(ctx, &candidate, &remote, settingCapabilities); syncDiags.HasError() {
				return schemahelpers.DiagnosticsError(syncDiags)
			}
			current, err := expandTableModel(ctx, candidate)
			if err != nil {
				return err
			}

			updatePlan := plannedTableUpdate{ReplaceAttrs: map[string]struct{}{}}
			if replicated && !firstInShard {
				actions, replace, err := planSettingsUpdate(current.Settings, desired.Settings, buildEngineUpdateStrategy(capabilities), settingCapabilities)
				if err != nil {
					return err
				}
				if replace {
					updatePlan.ReplaceAttrs["settings"] = struct{}{}
				}
				updatePlan.ActionGroups = actions
			} else {
				updatePlan, err = planTableUpdate(current, desired, capabilities, settingCapabilities)
				if err != nil {
					return err
				}
			}
			if !enginesEquivalent(remote.Engine, desired.Engine) {
				updatePlan.ReplaceAttrs["engine"] = struct{}{}
			}
			if len(updatePlan.ReplaceAttrs) > 0 {
				return fmt.Errorf("the table differs from the desired definition in attributes that cannot be changed in place: %s. Replace the table or change the configuration to match it",
					strings.Join(slices.Sorted(maps.Keys(updatePlan.ReplaceAttrs)), ", "))
			}

			for _, actionGroup := range updatePlan.ActionGroups {
				if err := node.Client.AlterTable(ctx, desired.Database, desired.Name, clusterName, actionGroup); err != nil {
					return err
				}
			}
			return nil
		},
	})
	diags.Append(convergeDiags...)
	if diags.HasError() {
		return nil, diags
	}

	state := plan
	state.Nodes = nodes
	schemahelpers.SyncObjectState(state.ClusterName, state.Database, state.Name, table.CreateStatement, &state.ID, &state.QualifiedName, &state.CreateStatement)

	return &state, diags
}

func unmanagedPatterns(ctx context.Context, model TableResourceModel) ([]*regexp.Regexp, []*regexp.Regexp, error) {
	columnPatterns, err := compilePatterns(ctx, "unmanaged_columns", model.UnmanagedColumns)
	if err != nil {
		return nil, nil, err
	}
	indexPatterns, err := compilePatterns(ctx, "unmanaged_indexes", model.UnmanagedIndexes)
	if err != nil {
		return nil, nil, err
	}

	return columnPatterns, indexPatterns, nil
}

func expandTableModel(ctx context.Context, plan TableResourceModel) (dbops.Table, error) {
	columns, diags := schemahelpers.ExpandColumns(ctx, plan.Columns)
	if diags.HasError() {
		return dbops.Table{}, schemahelpers.DiagnosticsError(diags)
	}

	table := dbops.Table{
		Database:    plan.Database.ValueString(),
		Name:        plan.Name.ValueString(),
		Engine:      plan.Engine.ValueString(),
		Columns:     columns,
		PartitionBy: plan.PartitionBy.ValueString(),
		OrderBy:     plan.OrderBy.ValueString(),
		PrimaryKey:  plan.PrimaryKey.ValueString(),
		SampleBy:    plan.SampleBy.ValueString(),
		TTL:         plan.TTL.ValueString(),
		Settings:    plan.Settings.ValueString(),
		AsSelect:    plan.AsSelect.ValueString(),
	}
	if diags := expandElements(ctx, plan, &table); diags.HasError() {
		return dbops.Table{}, schemahelpers.DiagnosticsError(diags)
	}

	return table, nil
}

func syncTableState(ctx context.Context, state *TableResourceModel, table *dbops.Table, settingCapabilities map[string]dbops.TableSettingCapability) diag.Diagnostics {
	var diags diag.Diagnostics

	current, err := expandTableModel(ctx, *state)
	diags.Append(schemahelpers.DiagnosticsFromErr("Invalid table state", err)...)
	if diags.HasError() {
		return diags
	}

	state.Engine = syncEquivalentString(state.Engine, table.Engine, enginesEquivalent)
	state.PartitionBy = syncEquivalentString(state.PartitionBy, table.PartitionBy, expressionsEqual)
	state.OrderBy = syncEquivalentString(state.OrderBy, table.OrderBy, expressionListsEqual)
	// A primary key that repeats the sorting key says nothing, and is not taken into state
	// when the configuration does not set one.
	if state.PrimaryKey.IsNull() && expressionListsEqual(table.PrimaryKey, table.OrderBy) {
		state.PrimaryKey = types.StringNull()
	} else {
		state.PrimaryKey = syncEquivalentString(state.PrimaryKey, table.PrimaryKey, expressionListsEqual)
	}
	state.SampleBy = syncEquivalentString(state.SampleBy, table.SampleBy, expressionsEqual)
	state.TTL = syncEquivalentString(state.TTL, table.TTL, ttlExpressionsEqual)
	state.Settings = syncRemoteSettings(state.Settings, table.Settings, settingCapabilities)
	state.AsSelect = syncManagedAsSelect(state.AsSelect, table.AsSelect)

	if !schemahelpers.ColumnsEqual(current.Columns, table.Columns) {
		var columnDiags diag.Diagnostics
		state.Columns, columnDiags = schemahelpers.ColumnsValue(ctx, table.Columns)
		diags.Append(columnDiags...)
	}
	diags.Append(syncElements(ctx, state, current, table)...)

	return diags
}

func syncEquivalentString(current types.String, remote string, equal func(string, string) bool) types.String {
	if !current.IsNull() && !current.IsUnknown() && equal(current.ValueString(), remote) {
		return current
	}
	if normalizeSQL(remote) == "" {
		return types.StringNull()
	}
	return types.StringValue(remote)
}

func syncManagedAsSelect(current types.String, remote string) types.String {
	if normalizeSQL(remote) == "" && !current.IsNull() && !current.IsUnknown() && normalizeSQL(current.ValueString()) != "" {
		return current
	}
	return syncEquivalentString(current, remote, expressionsEqual)
}

func settingsStringsEqual(left string, right string) bool {
	leftParsed, leftErr := parseSettings(left)
	rightParsed, rightErr := parseSettings(right)
	if leftErr == nil && rightErr == nil {
		return settingsEqual(leftParsed, rightParsed)
	}
	return normalizeSQL(left) == normalizeSQL(right)
}

func settingsStringsEqualWithCapabilities(left string, right string, capabilities map[string]dbops.TableSettingCapability) bool {
	leftParsed, leftErr := parseSettings(left)
	rightParsed, rightErr := parseSettings(right)
	if leftErr != nil || rightErr != nil {
		return normalizeSQL(left) == normalizeSQL(right)
	}
	return settingsEqual(filterReadonlySettings(leftParsed, capabilities), filterReadonlySettings(rightParsed, capabilities))
}

func syncRemoteSettings(current types.String, remote string, capabilities map[string]dbops.TableSettingCapability) types.String {
	if !current.IsNull() && !current.IsUnknown() && settingsStringsEqualWithCapabilities(current.ValueString(), remote, capabilities) {
		return current
	}
	if normalizeSQL(remote) == "" {
		return types.StringNull()
	}
	if current.IsNull() || (!current.IsUnknown() && normalizeSQL(current.ValueString()) == "") {
		if settingsAppearToBeDefaults(remote, capabilities) {
			return current
		}
	}
	return types.StringValue(remote)
}

func settingsAppearToBeDefaults(raw string, capabilities map[string]dbops.TableSettingCapability) bool {
	if len(capabilities) == 0 {
		return false
	}

	parsed, err := parseSettings(raw)
	if err != nil {
		return false
	}

	for _, setting := range parsed.ordered {
		capability, ok := capabilities[setting.Name]
		if !ok || !capability.Known || !capability.Readonly || normalizeSQL(capability.Default) != normalizeSQL(setting.Value) {
			return false
		}
	}

	return len(parsed.ordered) > 0
}

func filterReadonlySettings(parsed parsedSettings, capabilities map[string]dbops.TableSettingCapability) parsedSettings {
	if len(parsed.ordered) == 0 {
		return parsed
	}

	filtered := parsedSettings{
		ordered: make([]settingAssignment, 0, len(parsed.ordered)),
		values:  make(map[string]string, len(parsed.values)),
	}

	for _, setting := range parsed.ordered {
		capability, ok := capabilities[setting.Name]
		if ok && capability.Known && capability.Readonly {
			continue
		}
		filtered.ordered = append(filtered.ordered, setting)
		filtered.values[setting.Name] = setting.Value
	}

	return filtered
}

func enginesEquivalent(left string, right string) bool {
	return normalizeEngine(left) == normalizeEngine(right)
}

func normalizeEngine(value string) string {
	value = normalizeSQL(value)
	if strings.HasSuffix(value, "()") {
		return strings.TrimSuffix(value, "()")
	}
	return value
}

// alignColumnOrder reorders remote columns to follow the declared order. Columns that are not
// declared keep their relative order and go last.
func alignColumnOrder(remote []dbops.Column, declared []dbops.Column) []dbops.Column {
	byName := make(map[string]dbops.Column, len(remote))
	for _, column := range remote {
		byName[column.Name] = column
	}

	aligned := make([]dbops.Column, 0, len(remote))
	seen := make(map[string]struct{}, len(declared))
	for _, column := range declared {
		if existing, ok := byName[column.Name]; ok {
			aligned = append(aligned, existing)
			seen[column.Name] = struct{}{}
		}
	}
	for _, column := range remote {
		if _, ok := seen[column.Name]; !ok {
			aligned = append(aligned, column)
		}
	}

	return aligned
}
