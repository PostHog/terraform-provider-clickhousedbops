package table

import (
	"slices"
	"testing"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
)

func TestSQLPreviewPreservesPhysicalReplicaSchema(t *testing.T) {
	columns := func(names ...string) []dbops.Column {
		result := make([]dbops.Column, len(names))
		for i, name := range names {
			result[i] = dbops.Column{Name: name, Type: "UInt64"}
		}
		return result
	}
	for _, tc := range []struct {
		name                             string
		existing, current, desired, want []string
	}{
		{name: "add after a managed column", existing: []string{"id", "unmanaged", "last"}, current: []string{"id", "last"}, desired: []string{"id", "added", "last"}, want: []string{"id", "added", "unmanaged", "last"}},
		{name: "ignore physical order", existing: []string{"last", "unmanaged", "id"}, current: []string{"id", "last"}, desired: []string{"id", "added", "last"}, want: []string{"last", "unmanaged", "id", "added"}},
		{name: "drop managed only", existing: []string{"id", "unmanaged", "dropped"}, current: []string{"id", "dropped"}, desired: []string{"id"}, want: []string{"id", "unmanaged"}},
		{name: "rename keeps physical position", existing: []string{"old", "unmanaged"}, current: []string{"old"}, desired: []string{"new"}, want: []string{"new", "unmanaged"}},
		{name: "adds run before moves", existing: []string{"a", "b"}, current: []string{"a", "b"}, desired: []string{"b", "c", "a"}, want: []string{"b", "a", "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preview, err := previewUpdatedTable(dbops.Table{Columns: columns(tc.existing...)}, dbops.Table{Columns: columns(tc.current...)}, dbops.Table{Columns: columns(tc.desired...)})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, column := range preview.Columns {
				got = append(got, column.Name)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("physical column order %v, want %v", got, tc.want)
			}
		})
	}
	old := []dbops.Index{{Name: "unchanged"}, {Name: "unmanaged"}, {Name: "modified", Expression: "id"}}
	managed := []dbops.Index{old[0], old[2]}
	desired := []dbops.Index{{Name: "modified", Expression: "id + 1"}, {Name: "unchanged"}, {Name: "added"}}
	got := previewNamedSchema(old, managed, desired, indexName, indexEqual)
	if len(got) != 4 || got[0].Name != "unchanged" || got[1].Name != "unmanaged" || got[2].Name != "modified" || got[3].Name != "added" {
		t.Fatalf("physical index order was not preserved: %v", got)
	}
}
