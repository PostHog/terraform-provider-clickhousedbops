package materializedview

import "github.com/hashicorp/terraform-plugin-framework/path"

func nodeReplacementPaths(plan, remote MaterializedViewResourceModel) path.Paths {
	var replacements path.Paths

	for name, equal := range map[string]bool{
		attributeEngine: remote.Engine.Equal(plan.Engine), attributePartitionBy: remote.PartitionBy.Equal(plan.PartitionBy),
		attributeOrderBy: remote.OrderBy.Equal(plan.OrderBy), attributePrimaryKey: remote.PrimaryKey.Equal(plan.PrimaryKey),
		attributeSampleBy: remote.SampleBy.Equal(plan.SampleBy), attributeTtl: remote.TTL.Equal(plan.TTL),
		attributeSettings: remote.Settings.Equal(plan.Settings), attributePopulate: remote.Populate.Equal(plan.Populate),
		attributeToTable: remote.ToTable.Equal(plan.ToTable), attributeColumns: remote.Columns.Equal(plan.Columns),
		attributeToColumns: remote.ToColumns.Equal(plan.ToColumns),
	} {
		if !equal {
			replacements = append(replacements, path.Root(name))
		}
	}
	if (plan.ToTable.IsNull() || !plan.ClusterName.IsNull()) && !remote.Query.Equal(plan.Query) {
		replacements = append(replacements, path.Root("query"))
	}
	return replacements
}
