package materializedview

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/schemahelpers"
)

func TestSyncMaterializedViewStateAppliesToTableSignatureDrift(t *testing.T) {
	ctx := context.Background()
	toColumns, diags := schemahelpers.ColumnSignaturesValue(ctx, []dbops.Column{
		{Name: "team_id", Type: "UInt64", Nullable: false},
	})
	if diags.HasError() {
		t.Fatalf("ColumnSignaturesValue() diagnostics = %v", diags)
	}

	state := MaterializedViewResourceModel{
		ToTable:   types.StringValue("analytics.events_rollup"),
		ToColumns: toColumns,
		Query:     types.StringValue("SELECT team_id FROM analytics.events"),
	}

	remote := &dbops.MaterializedView{
		ToTable: "analytics.events_rollup",
		ToColumns: []dbops.Column{
			{Name: "team_id", Type: "UInt64", Nullable: false},
			{Name: "event_count", Type: "UInt64", Nullable: false},
		},
		Query: "SELECT team_id, count() AS event_count FROM analytics.events GROUP BY team_id",
	}

	diags = syncMaterializedViewState(ctx, &state, remote)
	if diags.HasError() {
		t.Fatalf("syncMaterializedViewState() diagnostics = %v", diags)
	}

	gotColumns, diags := schemahelpers.ExpandColumnSignatures(ctx, state.ToColumns)
	if diags.HasError() {
		t.Fatalf("ExpandColumnSignatures() diagnostics = %v", diags)
	}
	if len(gotColumns) != 2 {
		t.Fatalf("expected remote to_columns to replace drifted state, got %#v", gotColumns)
	}
	if state.Query.ValueString() != remote.Query {
		t.Fatalf("expected remote query to be applied, got %q", state.Query.ValueString())
	}
}

func TestSyncMaterializedViewStateSyncsPopulateFlag(t *testing.T) {
	ctx := context.Background()
	state := MaterializedViewResourceModel{
		Populate: types.BoolValue(true),
	}

	remote := &dbops.MaterializedView{
		Engine:   "MergeTree()",
		OrderBy:  "id",
		Populate: false,
		Query:    "SELECT id FROM analytics.events",
	}

	diags := syncMaterializedViewState(ctx, &state, remote)
	if diags.HasError() {
		t.Fatalf("syncMaterializedViewState() diagnostics = %v", diags)
	}
	if state.Populate.ValueBool() {
		t.Fatal("expected populate to be reset from remote state")
	}
}

func TestSyncMaterializedViewStateSyncsEngineBackedClauses(t *testing.T) {
	ctx := context.Background()
	state := MaterializedViewResourceModel{}

	remote := &dbops.MaterializedView{
		Engine:      "MergeTree()",
		PartitionBy: "toYYYYMM(created_at)",
		OrderBy:     "(team_id, created_at)",
		PrimaryKey:  "team_id",
		SampleBy:    "team_id",
		TTL:         "created_at + INTERVAL 1 DAY",
		Settings:    "index_granularity = 8192",
		Query:       "SELECT team_id, created_at FROM analytics.events",
	}

	diags := syncMaterializedViewState(ctx, &state, remote)
	if diags.HasError() {
		t.Fatalf("syncMaterializedViewState() diagnostics = %v", diags)
	}

	if state.Engine.ValueString() != remote.Engine || state.OrderBy.ValueString() != remote.OrderBy || state.Settings.ValueString() != remote.Settings {
		t.Fatalf("expected engine-backed clauses to sync from remote, got %#v", state)
	}
}

func TestSyncMaterializedViewStateKeepsConfiguredTextAndIgnoresInferredColumns(t *testing.T) {
	query := "SELECT\n    team_id,\n    count() AS c\nFROM analytics.events\nGROUP BY team_id"
	state := MaterializedViewResourceModel{
		ToTable:   types.StringValue("analytics.events_rollup"),
		ToColumns: types.ListNull(types.ObjectType{}),
		Columns:   types.ListNull(types.ObjectType{}),
		Query:     types.StringValue(query),
	}

	remote := &dbops.MaterializedView{
		ToTable:   "analytics.events_rollup",
		ToColumns: []dbops.Column{{Name: "team_id", Type: "UInt64"}, {Name: "c", Type: "UInt64"}},
		Query:     "SELECT team_id, count() AS c FROM analytics.events GROUP BY team_id",
	}

	diags := syncMaterializedViewState(context.Background(), &state, remote)
	if diags.HasError() {
		t.Fatalf("syncMaterializedViewState() diagnostics = %v", diags)
	}
	if state.Query.ValueString() != query {
		t.Fatalf("expected the configured query text to stay, got %q", state.Query.ValueString())
	}
	if !state.ToColumns.IsNull() {
		t.Fatalf("expected the inferred to_columns list to be ignored, got %#v", state.ToColumns)
	}
}
