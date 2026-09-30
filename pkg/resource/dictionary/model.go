package dictionary

import "github.com/hashicorp/terraform-plugin-framework/types"

type DictionaryResourceModel struct {
	ClusterName     types.String `tfsdk:"cluster_name"`
	ID              types.String `tfsdk:"id"`
	QualifiedName   types.String `tfsdk:"qualified_name"`
	Nodes           types.List   `tfsdk:"nodes"`
	CreateStatement types.String `tfsdk:"create_statement"`
	Database        types.String `tfsdk:"database"`
	Name            types.String `tfsdk:"name"`
	Attributes      types.List   `tfsdk:"attributes"`
	PrimaryKey      types.List   `tfsdk:"primary_key"`
	Source          types.String `tfsdk:"source"`
	Layout          types.String `tfsdk:"layout"`
	Lifetime        types.String `tfsdk:"lifetime"`
	Range           types.String `tfsdk:"range"`
	Settings        types.String `tfsdk:"settings"`
	Comment         types.String `tfsdk:"comment"`
}
