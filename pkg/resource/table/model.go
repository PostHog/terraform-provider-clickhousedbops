package table

import "github.com/hashicorp/terraform-plugin-framework/types"

type TableResourceModel struct {
	ClusterName      types.String `tfsdk:"cluster_name"`
	ID               types.String `tfsdk:"id"`
	QualifiedName    types.String `tfsdk:"qualified_name"`
	Nodes            types.List   `tfsdk:"nodes"`
	CreateStatement  types.String `tfsdk:"create_statement"`
	Database         types.String `tfsdk:"database"`
	Name             types.String `tfsdk:"name"`
	Engine           types.String `tfsdk:"engine"`
	Columns          types.List   `tfsdk:"columns"`
	Indexes          types.List   `tfsdk:"indexes"`
	Projections      types.List   `tfsdk:"projections"`
	Constraints      types.List   `tfsdk:"constraints"`
	UnmanagedColumns types.List   `tfsdk:"unmanaged_columns"`
	UnmanagedIndexes types.List   `tfsdk:"unmanaged_indexes"`
	PartitionBy      types.String `tfsdk:"partition_by"`
	OrderBy          types.String `tfsdk:"order_by"`
	PrimaryKey       types.String `tfsdk:"primary_key"`
	SampleBy         types.String `tfsdk:"sample_by"`
	TTL              types.String `tfsdk:"ttl"`
	Settings         types.String `tfsdk:"settings"`
	AsSelect         types.String `tfsdk:"as_select"`
}
