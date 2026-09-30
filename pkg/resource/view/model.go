package view

import "github.com/hashicorp/terraform-plugin-framework/types"

type ViewResourceModel struct {
	ClusterName     types.String `tfsdk:"cluster_name"`
	ID              types.String `tfsdk:"id"`
	QualifiedName   types.String `tfsdk:"qualified_name"`
	Nodes           types.List   `tfsdk:"nodes"`
	CreateStatement types.String `tfsdk:"create_statement"`
	Database        types.String `tfsdk:"database"`
	Name            types.String `tfsdk:"name"`
	Columns         types.List   `tfsdk:"columns"`
	Query           types.String `tfsdk:"query"`
}
