package schemahelpers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/querybuilder"
)

type ColumnModel struct {
	Name                   types.String `tfsdk:"name"`
	Type                   types.String `tfsdk:"type"`
	Nullable               types.Bool   `tfsdk:"nullable"`
	Comment                types.String `tfsdk:"comment"`
	DefaultExpression      types.String `tfsdk:"default_expression"`
	MaterializedExpression types.String `tfsdk:"materialized_expression"`
	AliasExpression        types.String `tfsdk:"alias_expression"`
	EphemeralExpression    types.String `tfsdk:"ephemeral_expression"`
	Codec                  types.String `tfsdk:"codec"`
	TTL                    types.String `tfsdk:"ttl"`
}

type ColumnSignatureModel struct {
	Name     types.String `tfsdk:"name"`
	Type     types.String `tfsdk:"type"`
	Nullable types.Bool   `tfsdk:"nullable"`
}

func ColumnsAttribute(description string) schema.ListNestedAttribute {
	return ColumnsAttributeWithPlanModifiers(description, []planmodifier.List{
		listplanmodifier.RequiresReplace(),
	})
}

func ColumnSignaturesAttribute(description string) schema.ListNestedAttribute {
	return ColumnSignaturesAttributeWithPlanModifiers(description, []planmodifier.List{
		listplanmodifier.RequiresReplace(),
	})
}

func ColumnsAttributeWithPlanModifiers(description string, modifiers []planmodifier.List) schema.ListNestedAttribute {
	attributes := columnSignatureAttributes()
	attributes["comment"] = schema.StringAttribute{
		Optional:    true,
		Description: "Optional column comment",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attributes["default_expression"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw SQL expression to use in a DEFAULT clause",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attributes["materialized_expression"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw SQL expression to use in a MATERIALIZED clause",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attributes["alias_expression"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw SQL expression to use in an ALIAS clause",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attributes["ephemeral_expression"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw SQL expression to use in an EPHEMERAL clause",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attributes["codec"] = schema.StringAttribute{
		Optional:    true,
		Description: "Compression codecs: the text inside CODEC(...), for example ZSTD(3) or Delta(4), ZSTD(1)",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
	attributes["ttl"] = schema.StringAttribute{
		Optional:    true,
		Description: "Raw column TTL expression",
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}

	return schema.ListNestedAttribute{
		Optional:    true,
		Description: description,
		Validators: []validator.List{
			listvalidator.SizeAtLeast(1),
		},
		PlanModifiers: modifiers,
		NestedObject: schema.NestedAttributeObject{
			Attributes: attributes,
		},
	}
}

func ColumnSignaturesAttributeWithPlanModifiers(description string, modifiers []planmodifier.List) schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional:    true,
		Description: description,
		Validators: []validator.List{
			listvalidator.SizeAtLeast(1),
		},
		PlanModifiers: modifiers,
		NestedObject: schema.NestedAttributeObject{
			Attributes: columnSignatureAttributes(),
		},
	}
}

func ExpandColumns(ctx context.Context, columns types.List) ([]dbops.Column, diag.Diagnostics) {
	var diags diag.Diagnostics

	if columns.IsNull() || columns.IsUnknown() {
		return nil, diags
	}

	var models []ColumnModel
	diags.Append(columns.ElementsAs(ctx, &models, false)...)
	if diags.HasError() {
		return nil, diags
	}

	ret := make([]dbops.Column, 0, len(models))
	for _, model := range models {
		column := dbops.Column{
			Name:     model.Name.ValueString(),
			Type:     model.Type.ValueString(),
			Nullable: model.Nullable.ValueBool(),
		}

		if !model.Comment.IsNull() {
			column.Comment = model.Comment.ValueString()
		}
		if !model.DefaultExpression.IsNull() {
			expr := model.DefaultExpression.ValueString()
			column.DefaultExpression = &expr
		}
		if !model.MaterializedExpression.IsNull() {
			expr := model.MaterializedExpression.ValueString()
			column.MaterializedExpression = &expr
		}
		if !model.AliasExpression.IsNull() {
			expr := model.AliasExpression.ValueString()
			column.AliasExpression = &expr
		}
		if !model.EphemeralExpression.IsNull() {
			expr := model.EphemeralExpression.ValueString()
			column.EphemeralExpression = &expr
		}
		column.Codec = model.Codec.ValueString()
		column.TTL = model.TTL.ValueString()

		ret = append(ret, column)
	}

	return ret, diags
}

func ExpandColumnSignatures(ctx context.Context, columns types.List) ([]dbops.Column, diag.Diagnostics) {
	var diags diag.Diagnostics

	if columns.IsNull() || columns.IsUnknown() {
		return nil, diags
	}

	var models []ColumnSignatureModel
	diags.Append(columns.ElementsAs(ctx, &models, false)...)
	if diags.HasError() {
		return nil, diags
	}

	ret := make([]dbops.Column, 0, len(models))
	for _, model := range models {
		ret = append(ret, dbops.Column{
			Name:     model.Name.ValueString(),
			Type:     model.Type.ValueString(),
			Nullable: model.Nullable.ValueBool(),
		})
	}

	return ret, diags
}

func ColumnsValue(ctx context.Context, columns []dbops.Column) (types.List, diag.Diagnostics) {
	if len(columns) == 0 {
		return types.ListNull(types.ObjectType{AttrTypes: columnObjectAttrTypes()}), nil
	}

	models := make([]ColumnModel, 0, len(columns))
	for _, column := range columns {
		model := ColumnModel{
			Name:     types.StringValue(column.Name),
			Type:     types.StringValue(column.Type),
			Nullable: nullableValue(column.Nullable),
			Codec:    SyncOptionalString(types.StringNull(), column.Codec),
			TTL:      SyncOptionalString(types.StringNull(), column.TTL),
		}

		if strings.TrimSpace(column.Comment) != "" {
			model.Comment = types.StringValue(column.Comment)
		} else {
			model.Comment = types.StringNull()
		}

		model.DefaultExpression = optionalStringValue(column.DefaultExpression)
		model.MaterializedExpression = optionalStringValue(column.MaterializedExpression)
		model.AliasExpression = optionalStringValue(column.AliasExpression)
		model.EphemeralExpression = optionalStringValue(column.EphemeralExpression)
		models = append(models, model)
	}

	return types.ListValueFrom(ctx, types.ObjectType{AttrTypes: columnObjectAttrTypes()}, models)
}

func ColumnSignaturesValue(ctx context.Context, columns []dbops.Column) (types.List, diag.Diagnostics) {
	if len(columns) == 0 {
		return types.ListNull(types.ObjectType{AttrTypes: columnSignatureObjectAttrTypes()}), nil
	}

	models := make([]ColumnSignatureModel, 0, len(columns))
	for _, column := range columns {
		models = append(models, ColumnSignatureModel{
			Name:     types.StringValue(column.Name),
			Type:     types.StringValue(column.Type),
			Nullable: nullableValue(column.Nullable),
		})
	}

	return types.ListValueFrom(ctx, types.ObjectType{AttrTypes: columnSignatureObjectAttrTypes()}, models)
}

func QualifiedName(database string, name string) string {
	return fmt.Sprintf("%s.%s", database, name)
}

func ObjectID(clusterName *string, database string, name string) string {
	if clusterName != nil && *clusterName != "" {
		return fmt.Sprintf("%s:%s", *clusterName, QualifiedName(database, name))
	}

	return QualifiedName(database, name)
}

func DiagnosticsError(diags diag.Diagnostics) error {
	if !diags.HasError() {
		return nil
	}

	var parts []string
	for _, d := range diags {
		if d.Severity() != diag.SeverityError {
			continue
		}
		msg := d.Summary()
		if detail := d.Detail(); detail != "" {
			msg += ": " + detail
		}
		parts = append(parts, msg)
	}
	return errors.New(strings.Join(parts, "; "))
}

func DiagnosticsFromErr(summary string, err error) diag.Diagnostics {
	var diags diag.Diagnostics
	if err != nil {
		diags.AddError(summary, err.Error())
	}
	return diags
}

func SyncObjectState(clusterName types.String, database types.String, name types.String, createStatement string, id *types.String, qualifiedName *types.String, createStatementAttr *types.String) {
	*id = types.StringValue(ObjectID(clusterName.ValueStringPointer(), database.ValueString(), name.ValueString()))
	*qualifiedName = types.StringValue(QualifiedName(database.ValueString(), name.ValueString()))

	if strings.TrimSpace(createStatement) != "" {
		*createStatementAttr = types.StringValue(createStatement)
	} else {
		*createStatementAttr = types.StringNull()
	}
}

func columnSignatureAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"name": schema.StringAttribute{
			Required:    true,
			Description: "Column name",
			Validators: []validator.String{
				stringvalidator.LengthAtLeast(1),
			},
		},
		"type": schema.StringAttribute{
			Required:    true,
			Description: "Column type definition. It can contain Nullable(...) verbatim, or leave it out and set nullable instead.",
			Validators: []validator.String{
				stringvalidator.LengthAtLeast(1),
			},
		},
		"nullable": schema.BoolAttribute{
			Optional:    true,
			Description: "Whether the provider should wrap the column type in Nullable(...). Defaults to false.",
		},
	}
}

func columnObjectAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"name":                    types.StringType,
		"type":                    types.StringType,
		"nullable":                types.BoolType,
		"comment":                 types.StringType,
		"default_expression":      types.StringType,
		"materialized_expression": types.StringType,
		"alias_expression":        types.StringType,
		"ephemeral_expression":    types.StringType,
		"codec":                   types.StringType,
		"ttl":                     types.StringType,
	}
}

func columnSignatureObjectAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"name":     types.StringType,
		"type":     types.StringType,
		"nullable": types.BoolType,
	}
}

// SQLEqual compares two SQL fragments ignoring whitespace differences outside quotes.
func SQLEqual(left string, right string) bool {
	return querybuilder.NormalizeSQL(left) == querybuilder.NormalizeSQL(right)
}

// TypesEqual compares the effective types of two columns, so that type "String" with
// nullable set equals type "Nullable(String)".
func TypesEqual(leftType string, leftNullable bool, rightType string, rightNullable bool) bool {
	return SQLEqual(querybuilder.EffectiveType(leftType, leftNullable), querybuilder.EffectiveType(rightType, rightNullable))
}

// OptionalStringsEqual compares two optional SQL fragments ignoring whitespace differences.
// A nil value and an empty value are equal only to themselves.
func OptionalStringsEqual(a *string, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return SQLEqual(*a, *b)
}

// ColumnEqual compares two columns for equality, ignoring whitespace differences in SQL fields.
func ColumnEqual(left dbops.Column, right dbops.Column) bool {
	return left.Name == right.Name &&
		TypesEqual(left.Type, left.Nullable, right.Type, right.Nullable) &&
		strings.TrimSpace(left.Comment) == strings.TrimSpace(right.Comment) &&
		OptionalStringsEqual(left.DefaultExpression, right.DefaultExpression) &&
		OptionalStringsEqual(left.MaterializedExpression, right.MaterializedExpression) &&
		OptionalStringsEqual(left.AliasExpression, right.AliasExpression) &&
		OptionalStringsEqual(left.EphemeralExpression, right.EphemeralExpression) &&
		SQLEqual(left.Codec, right.Codec) &&
		SQLEqual(left.TTL, right.TTL)
}

// ColumnsEqual compares two column slices for equality including all fields.
func ColumnsEqual(left []dbops.Column, right []dbops.Column) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !ColumnEqual(left[i], right[i]) {
			return false
		}
	}
	return true
}

// ColumnSignaturesEqual compares two column slices for equality considering
// only name, type, and nullable (the fields present in a column signature).
func ColumnSignaturesEqual(left []dbops.Column, right []dbops.Column) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Name != right[i].Name ||
			!TypesEqual(left[i].Type, left[i].Nullable, right[i].Type, right[i].Nullable) {
			return false
		}
	}
	return true
}

// SyncOptionalString updates a Terraform string attribute from a remote value,
// preserving null/unknown when the remote is empty.
func SyncOptionalString(current types.String, remote string) types.String {
	if strings.TrimSpace(remote) == "" {
		if !current.IsNull() && !current.IsUnknown() {
			return types.StringNull()
		}
		return current
	}
	return types.StringValue(remote)
}

// SyncEquivalentString updates a Terraform string attribute from a remote SQL fragment.
// It keeps the current text when that text is equivalent to the remote one.
func SyncEquivalentString(current types.String, remote string) types.String {
	if !current.IsNull() && !current.IsUnknown() && SQLEqual(current.ValueString(), remote) {
		return current
	}
	return SyncOptionalString(current, remote)
}

// nullableValue is null for a column that is not nullable: nullable is optional and a
// remote column always carries its full type.
func nullableValue(nullable bool) types.Bool {
	if nullable {
		return types.BoolValue(true)
	}
	return types.BoolNull()
}

func optionalStringValue(value *string) types.String {
	if value == nil || strings.TrimSpace(*value) == "" {
		return types.StringNull()
	}
	return types.StringValue(*value)
}

// CommonSchemaAttributes returns the schema attributes shared by all schema object resources
// (table, view, materialized_view, dictionary). The objectType parameter is used in descriptions.
func CommonSchemaAttributes(objectType string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"sql_plan_digest": schema.StringAttribute{Computed: true, Description: "Digest of the reviewed SQL operations. When enforce_sql_plan is enabled, an apply cannot expand a known SQL plan."},
		"cluster_name": schema.StringAttribute{
			Optional:    true,
			Description: fmt.Sprintf("Name of the cluster to create the %s into with ON CLUSTER. If omitted, the DDL runs only on the connected replica. Cannot be set when the provider sets fanout_cluster.", objectType),
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.RequiresReplace(),
			},
		},
		"id": schema.StringAttribute{
			Computed:    true,
			Description: fmt.Sprintf("Stable identifier in the form cluster:database.%s or database.%s", objectType, objectType),
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
			},
		},
		"qualified_name": schema.StringAttribute{
			Computed:    true,
			Description: fmt.Sprintf("Qualified name in the form database.%s", objectType),
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
			},
		},
		"nodes": schema.ListAttribute{
			Computed:    true,
			ElementType: types.StringType,
			Description: fmt.Sprintf("Hosts where the %s exists: every node of the provider's fanout_cluster, or the provider host.", objectType),
		},
		"create_statement": schema.StringAttribute{
			Computed:    true,
			Description: fmt.Sprintf("The CREATE %s statement as returned by ClickHouse", strings.ToUpper(objectType)),
		},
		"database": schema.StringAttribute{
			Required:    true,
			Description: "Database where the object resides",
			Validators: []validator.String{
				stringvalidator.LengthAtLeast(1),
			},
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.RequiresReplace(),
			},
		},
		"name": schema.StringAttribute{
			Required:    true,
			Description: fmt.Sprintf("%s name", titleCase(objectType)),
			Validators: []validator.String{
				stringvalidator.LengthAtLeast(1),
			},
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.RequiresReplace(),
			},
		},
	}
}

// ImportSchemaObjectState handles the common ImportState logic for schema objects.
// It parses the import ID in the format [cluster:]database.name and sets the
// database, name, and optionally cluster_name attributes on the state.
func ImportSchemaObjectState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ref := req.ID
	var clusterName *string
	if strings.Contains(ref, ":") {
		parts := strings.SplitN(ref, ":", 2)
		clusterName = &parts[0]
		ref = parts[1]
	}

	parts := strings.SplitN(ref, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected format: [cluster:]database.name, got: %s", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("database"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), parts[1])...)
	if clusterName != nil {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_name"), *clusterName)...)
	}
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
