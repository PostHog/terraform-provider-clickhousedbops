package querybuilder

import "testing"

func Test_createTable(t *testing.T) {
	clusterName := "cluster1"
	comment := "event name"
	defaultExpr := "now()"

	got, err := CreateTableQuery{
		Database:    "analytics",
		Name:        "events",
		ClusterName: &clusterName,
		Columns: []ColumnDefinition{
			{Name: "team_id", Type: "UInt64"},
			{Name: "event", Type: "String", Comment: &comment},
			{Name: "created_at", Type: "DateTime", DefaultExpression: &defaultExpr},
			{Name: "browser", Type: "String", Nullable: true},
		},
		Engine:      "MergeTree()",
		PartitionBy: "toYYYYMM(created_at)",
		OrderBy:     "(team_id, created_at)",
		Settings:    "index_granularity = 8192",
	}.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := "CREATE TABLE `analytics`.`events` ON CLUSTER 'cluster1' (`team_id` UInt64, `event` String COMMENT 'event name', `created_at` DateTime DEFAULT now(), `browser` Nullable(String)) ENGINE = MergeTree() PARTITION BY toYYYYMM(created_at) ORDER BY (team_id, created_at) SETTINGS index_granularity = 8192;"
	if got != want {
		t.Fatalf("Build() got = %v, want %v", got, want)
	}
}

func Test_createTableWithKafkaEngine(t *testing.T) {
	got, err := CreateTableQuery{
		Database: "analytics",
		Name:     "kafka_events",
		Columns: []ColumnDefinition{
			{Name: "event", Type: "String"},
		},
		Engine:   "Kafka('redpanda:9092', 'events', 'events_consumer', 'JSONEachRow')",
		Settings: "kafka_num_consumers = 1",
	}.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := "CREATE TABLE `analytics`.`kafka_events` (`event` String) ENGINE = Kafka('redpanda:9092', 'events', 'events_consumer', 'JSONEachRow') SETTINGS kafka_num_consumers = 1;"
	if got != want {
		t.Fatalf("Build() got = %v, want %v", got, want)
	}
}

func Test_createTableRejectsMultipleColumnExpressions(t *testing.T) {
	defaultExpr := "now()"
	aliasExpr := "created_at"

	_, err := CreateTableQuery{
		Database: "analytics",
		Name:     "events",
		Columns: []ColumnDefinition{
			{
				Name:              "created_at",
				Type:              "DateTime",
				DefaultExpression: &defaultExpr,
				AliasExpression:   &aliasExpr,
			},
		},
		Engine: "MergeTree()",
	}.Build()
	if err == nil {
		t.Fatal("expected Build() to fail when multiple column expressions are set")
	}
}

func Test_alterTable(t *testing.T) {
	comment := "human-readable"
	addAction, err := BuildAddColumnAction(ColumnDefinition{Name: "extra", Type: "UInt64", Comment: &comment}, AfterColumnPosition("event"))
	if err != nil {
		t.Fatalf("BuildAddColumnAction() error = %v", err)
	}
	orderByAction, err := BuildModifyOrderByAction("(team_id, extra)")
	if err != nil {
		t.Fatalf("BuildModifyOrderByAction() error = %v", err)
	}

	sql, err := BuildAlterTable("analytics", "events", nil, []string{
		addAction,
		orderByAction,
	})
	if err != nil {
		t.Fatalf("BuildAlterTable() error = %v", err)
	}

	want := "ALTER TABLE `analytics`.`events` ADD COLUMN IF NOT EXISTS `extra` UInt64 COMMENT 'human-readable' AFTER `event`, MODIFY ORDER BY (team_id, extra) SETTINGS alter_sync = 2;"
	if sql != want {
		t.Fatalf("BuildAlterTable() got = %v, want %v", sql, want)
	}
}

func Test_modifySettingAction(t *testing.T) {
	got, err := BuildModifySettingAction("ttl_only_drop_parts", "1")
	if err != nil {
		t.Fatalf("BuildModifySettingAction() error = %v", err)
	}

	want := "MODIFY SETTING `ttl_only_drop_parts` = 1"
	if got != want {
		t.Fatalf("BuildModifySettingAction() got = %v, want %v", got, want)
	}
}

func Test_createView(t *testing.T) {
	got, err := CreateViewQuery{
		Database: "analytics",
		Name:     "team_event_counts",
		Columns: []ColumnDefinition{
			{Name: "team_id", Type: "UInt64"},
			{Name: "event_count", Type: "UInt64"},
		},
		Query: "SELECT team_id, count() AS event_count FROM analytics.events GROUP BY team_id",
	}.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := "CREATE VIEW `analytics`.`team_event_counts` (`team_id` UInt64, `event_count` UInt64) AS SELECT team_id, count() AS event_count FROM analytics.events GROUP BY team_id;"
	if got != want {
		t.Fatalf("Build() got = %v, want %v", got, want)
	}
}

func Test_createMaterializedViewToTable(t *testing.T) {
	got, err := CreateMaterializedViewQuery{
		Database: "analytics",
		Name:     "events_mv",
		ToTable:  "analytics.daily_event_counts",
		ToColumns: []ColumnDefinition{
			{Name: "team_id", Type: "UInt64"},
			{Name: "event_date", Type: "Date"},
			{Name: "event_count", Type: "UInt64"},
		},
		Query: "SELECT team_id, toDate(created_at) AS event_date, count() AS event_count FROM analytics.events GROUP BY team_id, event_date",
	}.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := "CREATE MATERIALIZED VIEW `analytics`.`events_mv` TO `analytics`.`daily_event_counts` (`team_id` UInt64, `event_date` Date, `event_count` UInt64) AS SELECT team_id, toDate(created_at) AS event_date, count() AS event_count FROM analytics.events GROUP BY team_id, event_date;"
	if got != want {
		t.Fatalf("Build() got = %v, want %v", got, want)
	}
}

func Test_createMaterializedViewEngineBacked(t *testing.T) {
	got, err := CreateMaterializedViewQuery{
		Database:    "analytics",
		Name:        "events_mv",
		Engine:      "MergeTree()",
		PartitionBy: "toYYYYMM(created_at)",
		OrderBy:     "team_id",
		PrimaryKey:  "team_id",
		SampleBy:    "team_id",
		TTL:         "created_at + INTERVAL 1 DAY",
		Settings:    "index_granularity = 8192",
		Columns: []ColumnDefinition{
			{Name: "team_id", Type: "UInt64"},
			{Name: "event_count", Type: "UInt64"},
		},
		Query: "SELECT team_id, count() AS event_count FROM analytics.events GROUP BY team_id",
	}.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := "CREATE MATERIALIZED VIEW `analytics`.`events_mv` (`team_id` UInt64, `event_count` UInt64) ENGINE = MergeTree() PARTITION BY toYYYYMM(created_at) ORDER BY team_id PRIMARY KEY team_id SAMPLE BY team_id TTL created_at + INTERVAL 1 DAY SETTINGS index_granularity = 8192 AS SELECT team_id, count() AS event_count FROM analytics.events GROUP BY team_id;"
	if got != want {
		t.Fatalf("Build() got = %v, want %v", got, want)
	}
}

func Test_createMaterializedViewRequiresExactlyOneTargetMode(t *testing.T) {
	_, err := CreateMaterializedViewQuery{
		Database: "analytics",
		Name:     "events_mv",
		Engine:   "MergeTree()",
		ToTable:  "analytics.daily_event_counts",
		Query:    "SELECT 1",
	}.Build()
	if err == nil {
		t.Fatal("expected Build() to fail when both engine and to_table are set")
	}
}

func Test_createMaterializedViewRejectsColumnsWithToTable(t *testing.T) {
	_, err := CreateMaterializedViewQuery{
		Database: "analytics",
		Name:     "events_mv",
		Columns: []ColumnDefinition{
			{Name: "team_id", Type: "UInt64"},
		},
		ToTable: "analytics.daily_event_counts",
		Query:   "SELECT 1",
	}.Build()
	if err == nil {
		t.Fatal("expected Build() to fail when columns are set for a TO-backed materialized view")
	}
}

func Test_createMaterializedViewRejectsToColumnsWithEngine(t *testing.T) {
	_, err := CreateMaterializedViewQuery{
		Database: "analytics",
		Name:     "events_mv",
		Engine:   "MergeTree()",
		OrderBy:  "team_id",
		ToColumns: []ColumnDefinition{
			{Name: "team_id", Type: "UInt64"},
		},
		Query: "SELECT 1",
	}.Build()
	if err == nil {
		t.Fatal("expected Build() to fail when to_columns are set for an engine-backed materialized view")
	}
}

func Test_schemaObjectStatements(t *testing.T) {
	ephemeral := "upper(s)"
	index := IndexDefinition{Name: "idx", Expression: "s", Type: "bloom_filter(0.01)", Granularity: 2}
	projection := ProjectionDefinition{Name: "p", Query: "SELECT id, count() GROUP BY id"}
	constraint := ConstraintDefinition{Name: "c", Check: "id > 0"}

	createTable, err := CreateTableQuery{
		Database: "db",
		Name:     "t",
		Columns: []ColumnDefinition{
			{Name: "id", Type: "UInt64", Codec: "Delta(8), ZSTD(1)"},
			{Name: "s", Type: "Nullable(String)", Nullable: true, TTL: "ts + toIntervalDay(1)"},
			{Name: "e", Type: "String", EphemeralExpression: &ephemeral},
		},
		Indexes:     []IndexDefinition{index, {Name: "idx2", Expression: "id", Type: "minmax"}},
		Projections: []ProjectionDefinition{projection},
		Constraints: []ConstraintDefinition{constraint},
		Engine:      "MergeTree",
		OrderBy:     "id",
	}.Build()
	if err != nil {
		t.Fatalf("CreateTableQuery.Build() error = %v", err)
	}
	modifyQuery, err := BuildModifyQuery("db", "mv", " SELECT 1 ")
	if err != nil {
		t.Fatalf("BuildModifyQuery() error = %v", err)
	}
	replaceView, err := CreateViewQuery{Database: "db", Name: "v", OrReplace: true, Query: "SELECT 1"}.Build()
	if err != nil {
		t.Fatalf("CreateViewQuery.Build() error = %v", err)
	}

	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "create table with codec, column ttl, ephemeral, index, projection, constraint",
			got:  createTable,
			want: "CREATE TABLE `db`.`t` (`id` UInt64 CODEC(Delta(8), ZSTD(1)), `s` Nullable(String) TTL ts + toIntervalDay(1), `e` String EPHEMERAL upper(s), " +
				"INDEX `idx` s TYPE bloom_filter(0.01) GRANULARITY 2, INDEX `idx2` id TYPE minmax GRANULARITY 1, " +
				"PROJECTION `p` (SELECT id, count() GROUP BY id), CONSTRAINT `c` CHECK id > 0) ENGINE = MergeTree ORDER BY id;",
		},
		{name: "add index", got: BuildAddIndexAction(index), want: "ADD INDEX IF NOT EXISTS `idx` s TYPE bloom_filter(0.01) GRANULARITY 2"},
		{name: "drop index", got: BuildDropIndexAction("idx"), want: "DROP INDEX IF EXISTS `idx`"},
		{name: "add projection", got: BuildAddProjectionAction(projection), want: "ADD PROJECTION IF NOT EXISTS `p` (SELECT id, count() GROUP BY id)"},
		{name: "drop projection", got: BuildDropProjectionAction("p"), want: "DROP PROJECTION IF EXISTS `p`"},
		{name: "add constraint", got: BuildAddConstraintAction(constraint), want: "ADD CONSTRAINT IF NOT EXISTS `c` CHECK id > 0"},
		{name: "drop constraint", got: BuildDropConstraintAction("c"), want: "DROP CONSTRAINT IF EXISTS `c`"},
		{name: "remove codec", got: BuildRemoveColumnPropertyAction("s", "CODEC"), want: "MODIFY COLUMN `s` REMOVE CODEC"},
		{name: "modify query", got: modifyQuery, want: "ALTER TABLE `db`.`mv` MODIFY QUERY SELECT 1;"},
		{name: "create or replace view", got: replaceView, want: "CREATE OR REPLACE VIEW `db`.`v` AS SELECT 1;"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got  %s\nwant %s", tt.got, tt.want)
			}
		})
	}
}
