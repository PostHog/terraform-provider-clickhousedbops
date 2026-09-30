package table

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
)

type indexModel struct {
	Name        types.String `tfsdk:"name"`
	Expression  types.String `tfsdk:"expression"`
	Type        types.String `tfsdk:"type"`
	Granularity types.Int64  `tfsdk:"granularity"`
}

type projectionModel struct {
	Name     types.String `tfsdk:"name"`
	Query    types.String `tfsdk:"query"`
	Settings types.String `tfsdk:"settings"`
}

type constraintModel struct {
	Name  types.String `tfsdk:"name"`
	Check types.String `tfsdk:"check"`
}

var (
	indexObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"name":        types.StringType,
		"expression":  types.StringType,
		"type":        types.StringType,
		"granularity": types.Int64Type,
	}}
	projectionObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"name":     types.StringType,
		"query":    types.StringType,
		"settings": types.StringType,
	}}
	constraintObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"name":  types.StringType,
		"check": types.StringType,
	}}
)

func requiredSQLAttribute(description string) schema.StringAttribute {
	return schema.StringAttribute{
		Required:    true,
		Description: description,
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
		},
	}
}

func elementSchemaAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"indexes": schema.ListNestedAttribute{
			Optional:    true,
			Description: "Data skipping indexes. They are compared by name, not by position.",
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"name":       requiredSQLAttribute("Index name"),
					"expression": requiredSQLAttribute("Raw index expression"),
					"type":       requiredSQLAttribute("Index type, for example minmax or bloom_filter(0.01)"),
					"granularity": schema.Int64Attribute{
						Optional:    true,
						Description: "Index granularity. Defaults to 1.",
					},
				},
			},
		},
		"projections": schema.ListNestedAttribute{
			Optional:    true,
			Description: "Projections. They are compared by name, not by position.",
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"name":  requiredSQLAttribute("Projection name"),
					"query": requiredSQLAttribute("Raw projection query: the text inside PROJECTION name (...)"),
					"settings": schema.StringAttribute{
						Optional:    true,
						Description: "Projection settings: the text inside WITH SETTINGS (...)",
					},
				},
			},
		},
		"constraints": schema.ListNestedAttribute{
			Optional:    true,
			Description: "CHECK constraints. They are compared by name, not by position.",
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"name":  requiredSQLAttribute("Constraint name"),
					"check": requiredSQLAttribute("Raw CHECK expression"),
				},
			},
		},
		"unmanaged_columns": schema.ListAttribute{
			Optional:    true,
			ElementType: types.StringType,
			Description: "RE2 regular expressions. A remote column whose name matches any of them and that is not declared in columns is ignored: the provider does not report, change, or drop it. A pattern matches anywhere in the name unless it is anchored with ^ and $.",
		},
		"unmanaged_indexes": schema.ListAttribute{
			Optional:    true,
			ElementType: types.StringType,
			Description: "RE2 regular expressions. A remote index whose name matches any of them and that is not declared in indexes is ignored: the provider does not report, change, or drop it. A pattern matches anywhere in the name unless it is anchored with ^ and $.",
		},
	}
}

func expandElements(ctx context.Context, plan TableResourceModel, table *dbops.Table) diag.Diagnostics {
	var diags diag.Diagnostics

	if !plan.Indexes.IsNull() && !plan.Indexes.IsUnknown() {
		var models []indexModel
		diags.Append(plan.Indexes.ElementsAs(ctx, &models, false)...)
		for _, model := range models {
			table.Indexes = append(table.Indexes, dbops.Index{
				Name:        model.Name.ValueString(),
				Expression:  model.Expression.ValueString(),
				Type:        model.Type.ValueString(),
				Granularity: model.Granularity.ValueInt64(),
			})
		}
	}
	if !plan.Projections.IsNull() && !plan.Projections.IsUnknown() {
		var models []projectionModel
		diags.Append(plan.Projections.ElementsAs(ctx, &models, false)...)
		for _, model := range models {
			table.Projections = append(table.Projections, dbops.Projection{Name: model.Name.ValueString(), Query: model.Query.ValueString(), Settings: model.Settings.ValueString()})
		}
	}
	if !plan.Constraints.IsNull() && !plan.Constraints.IsUnknown() {
		var models []constraintModel
		diags.Append(plan.Constraints.ElementsAs(ctx, &models, false)...)
		for _, model := range models {
			table.Constraints = append(table.Constraints, dbops.Constraint{Name: model.Name.ValueString(), Check: model.Check.ValueString()})
		}
	}

	return diags
}

// syncElements writes the remote indexes, projections and constraints into state when they
// differ from what state holds.
func syncElements(ctx context.Context, state *TableResourceModel, current dbops.Table, remote *dbops.Table) diag.Diagnostics {
	var diags diag.Diagnostics

	if !namedListsEqual(current.Indexes, remote.Indexes, indexName, indexEqual) {
		models := make([]indexModel, 0, len(remote.Indexes))
		for _, index := range remote.Indexes {
			models = append(models, indexModel{
				Name:        types.StringValue(index.Name),
				Expression:  types.StringValue(index.Expression),
				Type:        types.StringValue(index.Type),
				Granularity: types.Int64Value(index.Granularity),
			})
		}
		state.Indexes = listOrNull(ctx, indexObjectType, models, &diags)
	}
	if !namedListsEqual(current.Projections, remote.Projections, projectionName, projectionEqual) {
		models := make([]projectionModel, 0, len(remote.Projections))
		for _, projection := range remote.Projections {
			models = append(models, projectionModel{Name: types.StringValue(projection.Name), Query: types.StringValue(projection.Query), Settings: optionalString(projection.Settings)})
		}
		state.Projections = listOrNull(ctx, projectionObjectType, models, &diags)
	}
	if !namedListsEqual(current.Constraints, remote.Constraints, constraintName, constraintEqual) {
		models := make([]constraintModel, 0, len(remote.Constraints))
		for _, constraint := range remote.Constraints {
			models = append(models, constraintModel{Name: types.StringValue(constraint.Name), Check: types.StringValue(constraint.Check)})
		}
		state.Constraints = listOrNull(ctx, constraintObjectType, models, &diags)
	}

	return diags
}

func listOrNull[T any](ctx context.Context, objectType types.ObjectType, models []T, diags *diag.Diagnostics) types.List {
	if len(models) == 0 {
		return types.ListNull(objectType)
	}
	list, listDiags := types.ListValueFrom(ctx, objectType, models)
	diags.Append(listDiags...)
	return list
}

func compilePatterns(ctx context.Context, attribute string, list types.List) ([]*regexp.Regexp, error) {
	if list.IsNull() || list.IsUnknown() {
		return nil, nil
	}

	var raw []string
	if diags := list.ElementsAs(ctx, &raw, false); diags.HasError() {
		return nil, fmt.Errorf("%s: invalid list of patterns", attribute)
	}

	patterns := make([]*regexp.Regexp, 0, len(raw))
	for _, expression := range raw {
		pattern, err := regexp.Compile(expression)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid regular expression %q: %w", attribute, expression, err)
		}
		patterns = append(patterns, pattern)
	}

	return patterns, nil
}

func optionalString(value string) types.String {
	if strings.TrimSpace(value) == "" {
		return types.StringNull()
	}
	return types.StringValue(value)
}
