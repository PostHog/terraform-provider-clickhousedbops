package dictionary

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
)

func TestSourcesEqual(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		want  bool
	}{
		{name: "password is hidden by ClickHouse", left: "CLICKHOUSE(USER 'u' PASSWORD 's3cret' DB 'd' TABLE 't')", right: "CLICKHOUSE(USER 'u' PASSWORD '[HIDDEN]' DB 'd' TABLE 't')", want: true},
		{name: "password with escaped quote", left: `CLICKHOUSE(PASSWORD 'a\'b' TABLE 't')`, right: "CLICKHOUSE(PASSWORD '[HIDDEN]' TABLE 't')", want: true},
		{name: "formatting outside strings", left: "CLICKHOUSE(\n  USER 'u'\n  TABLE 't'\n)", right: "CLICKHOUSE(USER 'u' TABLE 't')", want: true},
		{name: "no password on either side", left: "CLICKHOUSE(TABLE t DB 'd' USER 'u')", right: "CLICKHOUSE(TABLE t DB 'd' USER 'u')", want: true},
		{name: "query text with escapes must match exactly", left: `CLICKHOUSE(QUERY 'SELECT \'a\',\n b' PASSWORD 'x')`, right: `CLICKHOUSE(QUERY 'SELECT \'a\',\n b' PASSWORD '[HIDDEN]')`, want: true},
		{name: "query text differs", left: `CLICKHOUSE(QUERY 'SELECT a' PASSWORD 'x')`, right: `CLICKHOUSE(QUERY 'SELECT b' PASSWORD '[HIDDEN]')`},
		{name: "other field differs", left: "CLICKHOUSE(USER 'u' PASSWORD 'x')", right: "CLICKHOUSE(USER 'v' PASSWORD '[HIDDEN]')"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourcesEqual(tt.left, tt.right); got != tt.want {
				t.Errorf("sourcesEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSyncDictionaryStateKeepsEquivalentConfiguration(t *testing.T) {
	ctx := context.Background()
	attributes, diags := types.ListValueFrom(ctx, dictionaryAttributeObjectType(), []attributeModel{
		{Name: types.StringValue("id"), Type: types.StringValue("UInt64")},
		{Name: types.StringValue("n"), Type: types.StringValue("String"), Nullable: types.BoolValue(true)},
	})
	if diags.HasError() {
		t.Fatalf("ListValueFrom() diagnostics = %v", diags)
	}
	primaryKey, _ := types.ListValueFrom(ctx, types.StringType, []string{"id"})

	state := DictionaryResourceModel{
		Attributes: attributes,
		PrimaryKey: primaryKey,
		Source:     types.StringValue("CLICKHOUSE(USER 'u' PASSWORD 's3cret' TABLE 't')"),
		Layout:     types.StringValue("HASHED()"),
		Lifetime:   types.StringValue("MIN 0 MAX 300"),
		Range:      types.StringValue("MIN start MAX end"),
	}
	before := state

	remote := &dbops.Dictionary{
		Attributes: []dbops.DictionaryAttribute{{Name: "id", Type: "UInt64"}, {Name: "n", Type: "Nullable(String)"}},
		PrimaryKey: []string{"id"},
		Source:     "CLICKHOUSE(USER 'u' PASSWORD '[HIDDEN]' TABLE 't')",
		Layout:     "HASHED()",
		Lifetime:   "MIN 0 MAX 300",
		Range:      "MIN start MAX end",
	}

	if diags := syncDictionaryState(ctx, &state, remote); diags.HasError() {
		t.Fatalf("syncDictionaryState() diagnostics = %v", diags)
	}
	if !state.Source.Equal(before.Source) || !state.Attributes.Equal(before.Attributes) || !state.Range.Equal(before.Range) {
		t.Fatalf("expected the configured values to stay, got %#v", state)
	}

	remote.Attributes[1].Type = "String"
	remote.Range = ""
	if diags := syncDictionaryState(ctx, &state, remote); diags.HasError() {
		t.Fatalf("syncDictionaryState() diagnostics = %v", diags)
	}
	if state.Attributes.Equal(before.Attributes) || !state.Range.IsNull() {
		t.Fatalf("expected remote drift in attributes and range to reach state, got %#v", state)
	}
}
