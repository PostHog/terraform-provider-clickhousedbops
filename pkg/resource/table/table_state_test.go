package table

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/pkg/resource/schemahelpers"
)

func TestSyncTableStatePreservesEquivalentRemoteValues(t *testing.T) {
	ctx := context.Background()
	columns, diags := schemahelpers.ColumnsValue(ctx, []dbops.Column{
		{Name: "id", Type: "UInt64"},
		{Name: "ts", Type: "DateTime"},
	})
	if diags.HasError() {
		t.Fatalf("ColumnsValue() diagnostics = %v", diags)
	}

	state := TableResourceModel{
		Engine:      types.StringValue("MergeTree()"),
		Columns:     columns,
		OrderBy:     types.StringValue("(id, ts)"),
		PrimaryKey:  types.StringValue("(id, ts)"),
		TTL:         types.StringValue("ts + INTERVAL 1 DAY"),
		Settings:    types.StringValue("ttl_only_drop_parts = 1, index_granularity = 8192"),
		PartitionBy: types.StringValue("toYYYYMM(ts)"),
		SampleBy:    types.StringValue("id"),
	}

	remote := &dbops.Table{
		Engine:      "MergeTree",
		Columns:     []dbops.Column{{Name: "id", Type: "UInt64"}, {Name: "ts", Type: "DateTime"}},
		OrderBy:     "id, ts",
		PrimaryKey:  "id, ts",
		TTL:         "ts + toIntervalDay(1)",
		Settings:    "index_granularity = 8192, ttl_only_drop_parts = 1",
		PartitionBy: "toYYYYMM(ts)",
		SampleBy:    "id",
	}

	diags = syncTableState(ctx, &state, remote, map[string]dbops.TableSettingCapability{
		"ttl_only_drop_parts": {Name: "ttl_only_drop_parts", Known: true, Readonly: false},
		"index_granularity":   {Name: "index_granularity", Known: true, Readonly: true},
	})
	if diags.HasError() {
		t.Fatalf("syncTableState() diagnostics = %v", diags)
	}

	if state.Engine.ValueString() != "MergeTree()" {
		t.Fatalf("expected engine to preserve prior text, got %q", state.Engine.ValueString())
	}
	if state.OrderBy.ValueString() != "(id, ts)" {
		t.Fatalf("expected order_by to preserve prior text, got %q", state.OrderBy.ValueString())
	}
	if state.TTL.ValueString() != "ts + INTERVAL 1 DAY" {
		t.Fatalf("expected ttl to preserve prior text, got %q", state.TTL.ValueString())
	}
	if state.Settings.ValueString() != "ttl_only_drop_parts = 1, index_granularity = 8192" {
		t.Fatalf("expected settings to preserve prior text, got %q", state.Settings.ValueString())
	}
}

func TestSyncTableStateAppliesRemoteDrift(t *testing.T) {
	ctx := context.Background()
	columns, diags := schemahelpers.ColumnsValue(ctx, []dbops.Column{
		{Name: "id", Type: "UInt64"},
	})
	if diags.HasError() {
		t.Fatalf("ColumnsValue() diagnostics = %v", diags)
	}

	state := TableResourceModel{
		Columns:  columns,
		OrderBy:  types.StringValue("id"),
		Settings: types.StringNull(),
	}

	remote := &dbops.Table{
		Columns:  []dbops.Column{{Name: "id", Type: "UInt64"}, {Name: "ts", Type: "DateTime"}},
		OrderBy:  "(id, ts)",
		Settings: "ttl_only_drop_parts = 1",
	}

	diags = syncTableState(ctx, &state, remote, map[string]dbops.TableSettingCapability{
		"ttl_only_drop_parts": {Name: "ttl_only_drop_parts", Known: true, Readonly: false},
	})
	if diags.HasError() {
		t.Fatalf("syncTableState() diagnostics = %v", diags)
	}

	gotColumns, diags := schemahelpers.ExpandColumns(ctx, state.Columns)
	if diags.HasError() {
		t.Fatalf("ExpandColumns() diagnostics = %v", diags)
	}
	if len(gotColumns) != 2 {
		t.Fatalf("expected remote columns to replace drifted state, got %#v", gotColumns)
	}
	if state.OrderBy.ValueString() != "(id, ts)" {
		t.Fatalf("expected remote order_by to be applied, got %q", state.OrderBy.ValueString())
	}
	if state.Settings.ValueString() != "ttl_only_drop_parts = 1" {
		t.Fatalf("expected remote settings to be applied, got %q", state.Settings.ValueString())
	}
}

func TestSyncTableStateIgnoresDefaultReadonlySettingsWhenUnset(t *testing.T) {
	ctx := context.Background()
	state := TableResourceModel{
		Settings: types.StringNull(),
	}

	remote := &dbops.Table{
		Settings: "index_granularity = 8192",
	}

	diags := syncTableState(ctx, &state, remote, map[string]dbops.TableSettingCapability{
		"index_granularity": {Name: "index_granularity", Known: true, Readonly: true, Default: "8192"},
	})
	if diags.HasError() {
		t.Fatalf("syncTableState() diagnostics = %v", diags)
	}
	if !state.Settings.IsNull() {
		t.Fatalf("expected default readonly settings to be ignored, got %q", state.Settings.ValueString())
	}
}

func TestSyncTableStateIgnoresImplicitPrimaryKeyWhenUnset(t *testing.T) {
	ctx := context.Background()
	state := TableResourceModel{
		PrimaryKey: types.StringNull(),
	}

	remote := &dbops.Table{
		PrimaryKey: "id",
		OrderBy:    "id",
	}

	diags := syncTableState(ctx, &state, remote, nil)
	if diags.HasError() {
		t.Fatalf("syncTableState() diagnostics = %v", diags)
	}
	if !state.PrimaryKey.IsNull() {
		t.Fatalf("expected implicit primary key to be ignored, got %q", state.PrimaryKey.ValueString())
	}

	// A primary key shorter than the sorting key is a real part of the definition.
	state = TableResourceModel{PrimaryKey: types.StringNull()}
	diags = syncTableState(ctx, &state, &dbops.Table{PrimaryKey: "id", OrderBy: "(id, ts)"}, nil)
	if diags.HasError() {
		t.Fatalf("syncTableState() diagnostics = %v", diags)
	}
	if state.PrimaryKey.ValueString() != "id" {
		t.Fatalf("expected the explicit primary key in state, got %q", state.PrimaryKey.ValueString())
	}
}

func TestSyncTableStatePreservesManagedAsSelectWhenRemoteOmitsIt(t *testing.T) {
	ctx := context.Background()
	state := TableResourceModel{
		AsSelect: types.StringValue("SELECT * FROM source_table"),
	}

	remote := &dbops.Table{
		AsSelect: "",
	}

	diags := syncTableState(ctx, &state, remote, nil)
	if diags.HasError() {
		t.Fatalf("syncTableState() diagnostics = %v", diags)
	}
	if state.AsSelect.ValueString() != "SELECT * FROM source_table" {
		t.Fatalf("expected as_select to preserve prior text, got %q", state.AsSelect.ValueString())
	}
}
