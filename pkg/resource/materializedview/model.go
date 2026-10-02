package materializedview

import "github.com/hashicorp/terraform-plugin-framework/types"

type MaterializedViewResourceModel struct {
	SQLPlanDigest   types.String `tfsdk:"sql_plan_digest"`
	ClusterName     types.String `tfsdk:"cluster_name"`
	ID              types.String `tfsdk:"id"`
	QualifiedName   types.String `tfsdk:"qualified_name"`
	Nodes           types.List   `tfsdk:"nodes"`
	CreateStatement types.String `tfsdk:"create_statement"`
	Database        types.String `tfsdk:"database"`
	Name            types.String `tfsdk:"name"`
	Columns         types.List   `tfsdk:"columns"`
	Engine          types.String `tfsdk:"engine"`
	PartitionBy     types.String `tfsdk:"partition_by"`
	OrderBy         types.String `tfsdk:"order_by"`
	PrimaryKey      types.String `tfsdk:"primary_key"`
	SampleBy        types.String `tfsdk:"sample_by"`
	TTL             types.String `tfsdk:"ttl"`
	Settings        types.String `tfsdk:"settings"`
	Populate        types.Bool   `tfsdk:"populate"`
	ToTable         types.String `tfsdk:"to_table"`
	ToColumns       types.List   `tfsdk:"to_columns"`
	Query           types.String `tfsdk:"query"`
}
