package querybuilder

import "testing"

func Test_drop(t *testing.T) {
	cluster := "cluster1"

	tests := []struct {
		name    string
		builder DropQueryBuilder
		want    string
		wantErr bool
	}{
		{
			name:    "Drop dictionary on cluster",
			builder: NewDropDictionary("db1", "dict1").WithCluster(&cluster),
			want:    "DROP DICTIONARY IF EXISTS `db1`.`dict1` ON CLUSTER 'cluster1' SYNC;",
		},
		{
			name:    "Drop table",
			builder: NewDropTable("db1", "tbl1"),
			want:    "DROP TABLE IF EXISTS `db1`.`tbl1` SYNC;",
		},
		{
			name:    "Drop view",
			builder: NewDropView("db1", "view1"),
			want:    "DROP VIEW IF EXISTS `db1`.`view1` SYNC;",
		},
		{
			name:    "Drop materialized view",
			builder: NewDropMaterializedView("db1", "mv1"),
			want:    "DROP VIEW IF EXISTS `db1`.`mv1` SYNC;",
		},
		{
			name:    "Drop materialized view on cluster",
			builder: NewDropMaterializedView("db1", "mv1").WithCluster(&cluster),
			want:    "DROP VIEW IF EXISTS `db1`.`mv1` ON CLUSTER 'cluster1' SYNC;",
		},
		{
			name:    "Drop database",
			builder: NewDropDatabase("db1"),
			want:    "DROP DATABASE `db1`;",
		},
		{
			name:    "Drop database on cluster",
			builder: NewDropDatabase("db1").WithCluster(&cluster),
			want:    "DROP DATABASE `db1` ON CLUSTER 'cluster1';",
		},
		{
			name:    "Drop database with complex name",
			builder: NewDropDatabase("data`base"),
			want:    "DROP DATABASE `data\\`base`;",
		},
		{
			name:    "Fail to create role with empty name",
			builder: NewDropRole(""),
			wantErr: true,
		},
		{
			name:    "Drop role with simple name",
			builder: NewDropRole("role1"),
			want:    "DROP ROLE `role1`;",
		},
		{
			name:    "Drop role on cluster",
			builder: NewDropRole("role1").WithCluster(&cluster),
			want:    "DROP ROLE `role1` ON CLUSTER 'cluster1';",
		},
		{
			name:    "Drop role with complex name",
			builder: NewDropRole("ro`le1"),
			want:    "DROP ROLE `ro\\`le1`;",
		},
		{
			name:    "Fail to drop user with empty name",
			builder: NewDropUser(""),
			wantErr: true,
		},
		{
			name:    "Drop user with simple name",
			builder: NewDropUser("john"),
			want:    "DROP USER `john`;",
		},
		{
			name:    "Drop user with complex name",
			builder: NewDropUser("jo`hn"),
			want:    "DROP USER `jo\\`hn`;",
		},
		{
			name:    "Fail to drop dictionary with empty database",
			builder: NewDropDictionary("", "dict1"),
			wantErr: true,
		},
		{
			name:    "Fail to drop dictionary with empty name",
			builder: NewDropDictionary("db1", ""),
			wantErr: true,
		},
		{
			name:    "Fail to drop table with empty database",
			builder: NewDropTable("", "tbl1"),
			wantErr: true,
		},
		{
			name:    "Fail to drop table with empty name",
			builder: NewDropTable("db1", ""),
			wantErr: true,
		},
		{
			name:    "Fail to drop view with empty database",
			builder: NewDropView("", "view1"),
			wantErr: true,
		},
		{
			name:    "Fail to drop view with empty name",
			builder: NewDropView("db1", ""),
			wantErr: true,
		},
		{
			name:    "Drop named collection",
			builder: NewDropNamedCollection("collection1"),
			want:    "DROP NAMED COLLECTION `collection1`;",
		},
		{
			name:    "Drop named collection on cluster",
			builder: NewDropNamedCollection("collection1").WithCluster(&cluster),
			want:    "DROP NAMED COLLECTION `collection1` ON CLUSTER 'cluster1';",
		},
		{
			name:    "Drop named collection if exists",
			builder: NewDropNamedCollection("collection1").IfExists(true),
			want:    "DROP NAMED COLLECTION IF EXISTS `collection1`;",
		},
		{
			name:    "Drop named collection if exists on cluster",
			builder: NewDropNamedCollection("collection1").IfExists(true).WithCluster(&cluster),
			want:    "DROP NAMED COLLECTION IF EXISTS `collection1` ON CLUSTER 'cluster1';",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.builder.Build()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Build() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("Build() got = %v, want %v", got, tt.want)
			}
		})
	}
}
