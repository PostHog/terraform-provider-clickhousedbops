package dictionary

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/querybuilder"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/schemahelpers"
)

//go:embed dictionary.md
var dictionaryResourceDescription string

var (
	_ resource.Resource                = &Resource{}
	_ resource.ResourceWithConfigure   = &Resource{}
	_ resource.ResourceWithModifyPlan  = &Resource{}
	_ resource.ResourceWithImportState = &Resource{}
)

type attributeModel struct {
	Name              types.String `tfsdk:"name"`
	Type              types.String `tfsdk:"type"`
	Nullable          types.Bool   `tfsdk:"nullable"`
	DefaultExpression types.String `tfsdk:"default_expression"`
	Expression        types.String `tfsdk:"expression"`
	Hierarchical      types.Bool   `tfsdk:"hierarchical"`
	Injective         types.Bool   `tfsdk:"injective"`
	IsObjectID        types.Bool   `tfsdk:"is_object_id"`
}

func NewResource() resource.Resource {
	return &Resource{}
}

type Resource struct {
	client dbops.Client
}

func (r *Resource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dictionary"
}

func (r *Resource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := schemahelpers.CommonSchemaAttributes("dictionary")
	statement := attrs["create_statement"].(schema.StringAttribute)
	statement.Sensitive = true
	attrs["create_statement"] = statement
	attrs["attributes"] = schema.ListNestedAttribute{
		Required:    true,
		Description: "Dictionary attributes, including key columns referenced by primary_key. This can be assigned directly from a local list of objects.",
		Validators: []validator.List{
			listvalidator.SizeAtLeast(1),
		},
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{
					Required:    true,
					Description: "Attribute name",
					Validators: []validator.String{
						stringvalidator.LengthAtLeast(1),
					},
				},
				"type": schema.StringAttribute{
					Required:    true,
					Description: "Attribute type definition. It can contain Nullable(...) verbatim, or leave it out and set nullable instead.",
					Validators: []validator.String{
						stringvalidator.LengthAtLeast(1),
					},
				},
				"nullable": schema.BoolAttribute{
					Optional:    true,
					Description: "Whether the provider should wrap the attribute type in Nullable(...). Defaults to false.",
				},
				"default_expression": schema.StringAttribute{
					Optional:    true,
					Description: "Raw SQL expression to use in a DEFAULT clause",
					Validators: []validator.String{
						stringvalidator.LengthAtLeast(1),
					},
				},
				"expression": schema.StringAttribute{
					Optional:    true,
					Description: "Raw SQL expression to use in an EXPRESSION clause",
					Validators: []validator.String{
						stringvalidator.LengthAtLeast(1),
					},
				},
				"hierarchical": schema.BoolAttribute{
					Optional:    true,
					Description: "Whether to append the HIERARCHICAL modifier",
				},
				"injective": schema.BoolAttribute{
					Optional:    true,
					Description: "Whether to append the INJECTIVE modifier",
				},
				"is_object_id": schema.BoolAttribute{
					Optional:    true,
					Description: "Whether to append the IS_OBJECT_ID modifier",
				},
			},
		},
	}
	attrs["primary_key"] = schema.ListAttribute{
		Required:    true,
		ElementType: types.StringType,
		Description: "Ordered list of attribute names used in the PRIMARY KEY clause",
		Validators: []validator.List{
			listvalidator.SizeAtLeast(1),
		},
	}
	attrs["source"] = schema.StringAttribute{
		Sensitive:   true,
		Required:    true,
		Description: "Raw SOURCE clause body, for example CLICKHOUSE(HOST 'localhost' PORT tcpPort() USER 'default' PASSWORD 'test' DB 'analytics' TABLE 'teams_source') or NULL(). ClickHouse hides the PASSWORD value when it reports the dictionary, so a change of only the password is not detected.",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attrs["layout"] = schema.StringAttribute{
		Required:    true,
		Description: "Raw LAYOUT clause body, for example FLAT() or HASHED()",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attrs["lifetime"] = schema.StringAttribute{
		Required:    true,
		Description: "Raw LIFETIME clause body, for example 0 or MIN 0 MAX 300",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attrs["range"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw RANGE clause body for range dictionaries, for example MIN start_date MAX end_date",
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
	attrs["comment"] = schema.StringAttribute{
		Optional:    true,
		Description: "Comment associated with the dictionary",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	resp.Schema = schema.Schema{
		Attributes:          attrs,
		MarkdownDescription: dictionaryResourceDescription,
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
	if resp.Diagnostics.HasError() || req.State.Raw.IsNull() {
		return
	}

	var plan, state DictionaryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, converged, diags := schemahelpers.PlanNodeStates(ctx, r.client, plan,
		func(ctx context.Context, client dbops.Client) (*dbops.Dictionary, error) {
			return client.GetDictionary(ctx, plan.Database.ValueString(), plan.Name.ValueString(), plan.ClusterName.ValueStringPointer())
		}, func(ctx context.Context, state *DictionaryResourceModel, dict *dbops.Dictionary) diag.Diagnostics {
			return syncDictionaryState(ctx, state, dict, r.sourceComparison())
		})
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !converged {
		state.CreateStatement = types.StringUnknown()
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("create_statement"), types.StringUnknown())...)
	}
	planned, err := expandDictionaryModel(ctx, plan)
	if err != nil {
		return
	}
	current, err := expandDictionaryModel(ctx, state)
	if err != nil {
		return
	}
	if !attributesEqual(planned.Attributes, current.Attributes) || !slices.Equal(planned.PrimaryKey, current.PrimaryKey) || !plan.Comment.Equal(state.Comment) {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("attributes"), state.Attributes)...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("primary_key"), state.PrimaryKey)...)
	resp.Diagnostics.Append(schemahelpers.KeepEquivalentStrings(ctx, &resp.Plan, state.Nodes, state.CreateStatement, []schemahelpers.EquivalentString{
		{Attribute: "source", Planned: plan.Source, State: state.Source, Equal: r.sourceComparison()},
		{Attribute: "layout", Planned: plan.Layout, State: state.Layout},
		{Attribute: "lifetime", Planned: plan.Lifetime, State: state.Lifetime},
		{Attribute: "range", Planned: plan.Range, State: state.Range},
		{Attribute: "settings", Planned: plan.Settings, State: state.Settings},
	})...)
}

func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DictionaryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	state, diags := r.convergeDictionary(ctx, plan, r.client.AdoptExisting())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DictionaryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newState, dictionary, nodes, diags := schemahelpers.ReadNodes(ctx, r.client, state,
		func(ctx context.Context, client dbops.Client) (*dbops.Dictionary, error) {
			return client.GetDictionary(ctx, state.Database.ValueString(), state.Name.ValueString(), state.ClusterName.ValueStringPointer())
		},
		func(ctx context.Context, state *DictionaryResourceModel, dict *dbops.Dictionary) diag.Diagnostics {
			return syncDictionaryState(ctx, state, dict, r.sourceComparison())
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
	schemahelpers.SyncObjectState(newState.ClusterName, newState.Database, newState.Name, dictionary.CreateStatement, &newState.ID, &newState.QualifiedName, &newState.CreateStatement)
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan DictionaryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	state, diags := r.convergeDictionary(ctx, plan, true)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DictionaryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(schemahelpers.DeleteNodes(ctx, r.client, func(ctx context.Context, client dbops.Client) error {
		return client.DeleteDictionary(ctx, state.Database.ValueString(), state.Name.ValueString(), state.ClusterName.ValueStringPointer())
	})...)
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	schemahelpers.ImportSchemaObjectState(ctx, req, resp)
}

// convergeDictionary makes every node hold the planned dictionary. A dictionary that
// differs is replaced in place with CREATE OR REPLACE DICTIONARY.
func (r *Resource) convergeDictionary(ctx context.Context, plan DictionaryResourceModel, adopt bool) (*DictionaryResourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	desired, err := expandDictionaryModel(ctx, plan)
	diags.Append(schemahelpers.DiagnosticsFromErr("Invalid dictionary configuration", err)...)
	if diags.HasError() {
		return nil, diags
	}

	clusterName := plan.ClusterName.ValueStringPointer()
	dictionary, nodes, convergeDiags := schemahelpers.ConvergeNodes(ctx, r.client, adopt, schemahelpers.NodeConverger[dbops.Dictionary]{
		Kind:          "dictionary",
		QualifiedName: schemahelpers.QualifiedName(desired.Database, desired.Name),
		Get: func(ctx context.Context, client dbops.Client) (*dbops.Dictionary, error) {
			return client.GetDictionary(ctx, desired.Database, desired.Name, clusterName)
		},
		Create: func(ctx context.Context, client dbops.Client, _ *dbops.Dictionary) error {
			_, err := client.CreateDictionary(ctx, desired, clusterName)
			return err
		},
		Reconcile: func(ctx context.Context, node dbops.SchemaNode, _ bool, existing *dbops.Dictionary) error {
			candidate := plan
			if syncDiags := syncDictionaryState(ctx, &candidate, existing, r.sourceComparison()); syncDiags.HasError() {
				return schemahelpers.DiagnosticsError(syncDiags)
			}
			if reflect.DeepEqual(candidate, plan) {
				return nil
			}
			_, err := node.Client.ReplaceDictionary(ctx, desired, clusterName)
			return err
		},
	})
	diags.Append(convergeDiags...)
	if diags.HasError() {
		return nil, diags
	}

	state := plan
	state.Nodes = nodes
	schemahelpers.SyncObjectState(state.ClusterName, state.Database, state.Name, dictionary.CreateStatement, &state.ID, &state.QualifiedName, &state.CreateStatement)

	return &state, diags
}

var sourcePasswordPattern = regexp.MustCompile(`(?i)\bPASSWORD\s+'(?:[^'\\]|\\.|'')*'`)

// sourcesEqual compares two SOURCE clause bodies with the password value masked, because
// ClickHouse reports PASSWORD '[HIDDEN]'.
func sourcesEqual(left string, right string) bool {
	mask := func(source string) string {
		return sourcePasswordPattern.ReplaceAllString(querybuilder.NormalizeSQL(source), "PASSWORD '[HIDDEN]'")
	}
	return mask(left) == mask(right)
}

// sourcesEqualWithPasswords compares two SOURCE clause bodies as written, password included.
func sourcesEqualWithPasswords(left string, right string) bool {
	return querybuilder.NormalizeSQL(left) == querybuilder.NormalizeSQL(right)
}

// sourceComparison is how the provider compares sources: with passwords when it manages them.
func (r *Resource) sourceComparison() func(string, string) bool {
	if r.client != nil && r.client.ManageDictionaryPasswords() {
		return sourcesEqualWithPasswords
	}
	return sourcesEqual
}

func syncDictionaryState(ctx context.Context, state *DictionaryResourceModel, dict *dbops.Dictionary, equalSources func(string, string) bool) diag.Diagnostics {
	var diags diag.Diagnostics

	state.Comment = schemahelpers.SyncOptionalString(state.Comment, dict.Comment)
	if state.Source.IsNull() || state.Source.IsUnknown() || !equalSources(state.Source.ValueString(), dict.Source) {
		state.Source = schemahelpers.SyncOptionalString(state.Source, dict.Source)
	}
	state.Layout = schemahelpers.SyncEquivalentString(state.Layout, dict.Layout)
	state.Lifetime = schemahelpers.SyncEquivalentString(state.Lifetime, dict.Lifetime)
	state.Range = schemahelpers.SyncEquivalentString(state.Range, dict.Range)
	state.Settings = schemahelpers.SyncEquivalentString(state.Settings, dict.Settings)

	// The configured attributes and primary key stay in state when they are equivalent to
	// the remote ones, so that formatting differences are not drift.
	if current, err := expandDictionaryModel(ctx, *state); err == nil && attributesEqual(current.Attributes, dict.Attributes) && slices.Equal(current.PrimaryKey, dict.PrimaryKey) {
		return diags
	}

	attrModels := make([]attributeModel, 0, len(dict.Attributes))
	for _, attr := range dict.Attributes {
		model := attributeModel{
			Name:         types.StringValue(attr.Name),
			Type:         types.StringValue(attr.Type),
			Nullable:     types.BoolNull(),
			Hierarchical: trueOrNull(attr.Hierarchical),
			Injective:    trueOrNull(attr.Injective),
			IsObjectID:   trueOrNull(attr.IsObjectID),
		}
		if attr.DefaultExpression != nil {
			model.DefaultExpression = types.StringValue(*attr.DefaultExpression)
		} else {
			model.DefaultExpression = types.StringNull()
		}
		if attr.Expression != nil {
			model.Expression = types.StringValue(*attr.Expression)
		} else {
			model.Expression = types.StringNull()
		}
		attrModels = append(attrModels, model)
	}

	attrList, attrDiags := types.ListValueFrom(ctx, dictionaryAttributeObjectType(), attrModels)
	diags.Append(attrDiags...)
	if diags.HasError() {
		return diags
	}
	state.Attributes = attrList

	pkList, pkDiags := types.ListValueFrom(ctx, types.StringType, dict.PrimaryKey)
	diags.Append(pkDiags...)
	if diags.HasError() {
		return diags
	}
	state.PrimaryKey = pkList

	return diags
}

// trueOrNull is null for an unset flag: the flags are optional, and a configuration
// normally leaves them out.
func trueOrNull(value bool) types.Bool {
	if value {
		return types.BoolValue(true)
	}
	return types.BoolNull()
}

func attributesEqual(left []dbops.DictionaryAttribute, right []dbops.DictionaryAttribute) bool {
	return slices.EqualFunc(left, right, func(a dbops.DictionaryAttribute, b dbops.DictionaryAttribute) bool {
		return a.Name == b.Name &&
			schemahelpers.TypesEqual(a.Type, a.Nullable, b.Type, b.Nullable) &&
			schemahelpers.OptionalStringsEqual(a.DefaultExpression, b.DefaultExpression) &&
			schemahelpers.OptionalStringsEqual(a.Expression, b.Expression) &&
			a.Hierarchical == b.Hierarchical && a.Injective == b.Injective && a.IsObjectID == b.IsObjectID
	})
}

func dictionaryAttributeObjectType() types.ObjectType {
	return types.ObjectType{
		AttrTypes: map[string]attr.Type{
			"name":               types.StringType,
			"type":               types.StringType,
			"nullable":           types.BoolType,
			"default_expression": types.StringType,
			"expression":         types.StringType,
			"hierarchical":       types.BoolType,
			"injective":          types.BoolType,
			"is_object_id":       types.BoolType,
		},
	}
}

func expandDictionaryModel(ctx context.Context, plan DictionaryResourceModel) (dbops.Dictionary, error) {
	if plan.Attributes.IsNull() || plan.Attributes.IsUnknown() {
		return dbops.Dictionary{}, errors.New("attributes cannot be null")
	}

	var attributeModels []attributeModel
	diags := plan.Attributes.ElementsAs(ctx, &attributeModels, false)
	if diags.HasError() {
		return dbops.Dictionary{}, schemahelpers.DiagnosticsError(diags)
	}

	attributes := make([]dbops.DictionaryAttribute, 0, len(attributeModels))
	attributeNames := make(map[string]struct{}, len(attributeModels))
	for _, model := range attributeModels {
		name := model.Name.ValueString()
		if _, exists := attributeNames[name]; exists {
			return dbops.Dictionary{}, fmt.Errorf("duplicate dictionary attribute %q", name)
		}
		attributeNames[name] = struct{}{}

		attribute := dbops.DictionaryAttribute{
			Name:         name,
			Type:         model.Type.ValueString(),
			Nullable:     model.Nullable.ValueBool(),
			Hierarchical: model.Hierarchical.ValueBool(),
			Injective:    model.Injective.ValueBool(),
			IsObjectID:   model.IsObjectID.ValueBool(),
		}

		if !model.DefaultExpression.IsNull() {
			expr := model.DefaultExpression.ValueString()
			attribute.DefaultExpression = &expr
		}
		if !model.Expression.IsNull() {
			expr := model.Expression.ValueString()
			attribute.Expression = &expr
		}

		attributes = append(attributes, attribute)
	}

	var primaryKey []string
	diags = plan.PrimaryKey.ElementsAs(ctx, &primaryKey, false)
	if diags.HasError() {
		return dbops.Dictionary{}, schemahelpers.DiagnosticsError(diags)
	}
	for _, primaryKeyName := range primaryKey {
		if _, exists := attributeNames[primaryKeyName]; !exists {
			return dbops.Dictionary{}, fmt.Errorf("primary key attribute %q is not defined in attributes", primaryKeyName)
		}
	}

	return dbops.Dictionary{
		Database:   plan.Database.ValueString(),
		Name:       plan.Name.ValueString(),
		Attributes: attributes,
		PrimaryKey: primaryKey,
		Source:     plan.Source.ValueString(),
		Layout:     plan.Layout.ValueString(),
		Lifetime:   plan.Lifetime.ValueString(),
		Range:      plan.Range.ValueString(),
		Settings:   plan.Settings.ValueString(),
		Comment:    plan.Comment.ValueString(),
	}, nil
}
