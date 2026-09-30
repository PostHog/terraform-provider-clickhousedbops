package schemahelpers

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
)

func strPtr(s string) *string { return &s }

func TestOptionalStringsEqual(t *testing.T) {
	tests := []struct {
		name string
		a    *string
		b    *string
		want bool
	}{
		{"both nil", nil, nil, true},
		{"left nil right empty", nil, strPtr(""), false},
		{"left nil right set", nil, strPtr("x"), false},
		{"left set right nil", strPtr("x"), nil, false},
		{"equal values", strPtr("now()"), strPtr("now()"), true},
		{"equal after trim", strPtr(" now() "), strPtr("now()"), true},
		{"different values", strPtr("now()"), strPtr("today()"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OptionalStringsEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("OptionalStringsEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestColumnEqual(t *testing.T) {
	base := dbops.Column{Name: "id", Type: "UInt64", Nullable: false}

	t.Run("identical columns", func(t *testing.T) {
		if !ColumnEqual(base, base) {
			t.Fatal("expected equal")
		}
	})

	t.Run("type whitespace ignored", func(t *testing.T) {
		other := base
		other.Type = " UInt64 "
		if !ColumnEqual(base, other) {
			t.Fatal("expected equal after trimming type whitespace")
		}
	})

	t.Run("different type", func(t *testing.T) {
		other := base
		other.Type = "String"
		if ColumnEqual(base, other) {
			t.Fatal("expected not equal with different type")
		}
	})

	t.Run("nullable differs", func(t *testing.T) {
		other := base
		other.Nullable = true
		if ColumnEqual(base, other) {
			t.Fatal("expected not equal with different nullable")
		}
	})

	t.Run("comment differs", func(t *testing.T) {
		other := base
		other.Comment = "user id"
		if ColumnEqual(base, other) {
			t.Fatal("expected not equal with different comment")
		}
	})

	t.Run("default expression nil vs set", func(t *testing.T) {
		other := base
		other.DefaultExpression = strPtr("0")
		if ColumnEqual(base, other) {
			t.Fatal("expected not equal when one has default expression")
		}
	})

	t.Run("default expression equal after trim", func(t *testing.T) {
		a := base
		a.DefaultExpression = strPtr(" now() ")
		b := base
		b.DefaultExpression = strPtr("now()")
		if !ColumnEqual(a, b) {
			t.Fatal("expected equal after trimming default expression")
		}
	})

	t.Run("effective type, codec, ttl and ephemeral", func(t *testing.T) {
		tests := []struct {
			name  string
			left  dbops.Column
			right dbops.Column
			want  bool
		}{
			{name: "nullable flag equals verbatim Nullable type", left: dbops.Column{Name: "id", Type: "String", Nullable: true}, right: dbops.Column{Name: "id", Type: "Nullable(String)"}, want: true},
			{name: "nullable flag on a Nullable type is not wrapped twice", left: dbops.Column{Name: "id", Type: "Nullable(String)", Nullable: true}, right: dbops.Column{Name: "id", Type: "Nullable(String)"}, want: true},
			{name: "type formatting", left: dbops.Column{Name: "id", Type: "Tuple(\n  a String,\n  b UInt64\n)"}, right: dbops.Column{Name: "id", Type: "Tuple(a String, b UInt64)"}, want: true},
			{name: "codec formatting", left: dbops.Column{Name: "id", Type: "UInt64", Codec: "Delta(8),ZSTD(1)"}, right: dbops.Column{Name: "id", Type: "UInt64", Codec: "Delta(8), ZSTD(1)"}, want: true},
			{name: "codec differs", left: dbops.Column{Name: "id", Type: "UInt64", Codec: "ZSTD(1)"}, right: dbops.Column{Name: "id", Type: "UInt64", Codec: "ZSTD(3)"}},
			{name: "ttl differs", left: dbops.Column{Name: "id", Type: "UInt64", TTL: "ts + toIntervalDay(1)"}, right: dbops.Column{Name: "id", Type: "UInt64"}},
			{name: "ephemeral differs from default", left: dbops.Column{Name: "id", Type: "UInt64", EphemeralExpression: strPtr("1")}, right: dbops.Column{Name: "id", Type: "UInt64", DefaultExpression: strPtr("1")}},
			{name: "whitespace inside a string literal matters", left: dbops.Column{Name: "id", Type: "String", DefaultExpression: strPtr("'a  b'")}, right: dbops.Column{Name: "id", Type: "String", DefaultExpression: strPtr("'a b'")}},
		}
		for _, tt := range tests {
			if got := ColumnEqual(tt.left, tt.right); got != tt.want {
				t.Errorf("%s: ColumnEqual() = %v, want %v", tt.name, got, tt.want)
			}
		}
	})

	t.Run("materialized vs alias", func(t *testing.T) {
		a := base
		a.MaterializedExpression = strPtr("x")
		b := base
		b.AliasExpression = strPtr("x")
		if ColumnEqual(a, b) {
			t.Fatal("expected not equal: materialized vs alias")
		}
	})
}

func TestColumnsEqual(t *testing.T) {
	cols := []dbops.Column{
		{Name: "id", Type: "UInt64"},
		{Name: "name", Type: "String", Nullable: true, Comment: "user name"},
	}

	t.Run("equal slices", func(t *testing.T) {
		other := make([]dbops.Column, len(cols))
		copy(other, cols)
		if !ColumnsEqual(cols, other) {
			t.Fatal("expected equal")
		}
	})

	t.Run("different lengths", func(t *testing.T) {
		if ColumnsEqual(cols, cols[:1]) {
			t.Fatal("expected not equal with different lengths")
		}
	})

	t.Run("different order", func(t *testing.T) {
		reversed := []dbops.Column{cols[1], cols[0]}
		if ColumnsEqual(cols, reversed) {
			t.Fatal("expected not equal with different order")
		}
	})

	t.Run("nil vs empty", func(t *testing.T) {
		if !ColumnsEqual(nil, nil) {
			t.Fatal("expected nil == nil")
		}
		if ColumnsEqual(nil, cols) {
			t.Fatal("expected nil != non-empty")
		}
	})
}

func TestColumnSignaturesEqual(t *testing.T) {
	t.Run("ignores comment and expressions", func(t *testing.T) {
		left := []dbops.Column{{Name: "id", Type: "UInt64", Comment: "pk", DefaultExpression: strPtr("0")}}
		right := []dbops.Column{{Name: "id", Type: "UInt64", Comment: "different", AliasExpression: strPtr("x")}}
		if !ColumnSignaturesEqual(left, right) {
			t.Fatal("expected equal: signatures only compare name, type, nullable")
		}
	})

	t.Run("nullable matters", func(t *testing.T) {
		left := []dbops.Column{{Name: "id", Type: "UInt64", Nullable: false}}
		right := []dbops.Column{{Name: "id", Type: "UInt64", Nullable: true}}
		if ColumnSignaturesEqual(left, right) {
			t.Fatal("expected not equal with different nullable")
		}
	})

	t.Run("type whitespace ignored", func(t *testing.T) {
		left := []dbops.Column{{Name: "id", Type: " UInt64 "}}
		right := []dbops.Column{{Name: "id", Type: "UInt64"}}
		if !ColumnSignaturesEqual(left, right) {
			t.Fatal("expected equal after trimming type whitespace")
		}
	})
}

func TestSyncOptionalString(t *testing.T) {
	t.Run("remote set overwrites current", func(t *testing.T) {
		got := SyncOptionalString(types.StringValue("old"), "new")
		if got.ValueString() != "new" {
			t.Fatalf("got %q, want %q", got.ValueString(), "new")
		}
	})

	t.Run("remote set overwrites null", func(t *testing.T) {
		got := SyncOptionalString(types.StringNull(), "new")
		if got.IsNull() || got.ValueString() != "new" {
			t.Fatalf("got null=%v value=%q, want 'new'", got.IsNull(), got.ValueString())
		}
	})

	t.Run("remote empty nulls a set value", func(t *testing.T) {
		got := SyncOptionalString(types.StringValue("old"), "")
		if !got.IsNull() {
			t.Fatalf("expected null when remote is empty and current was set, got %q", got.ValueString())
		}
	})

	t.Run("remote empty preserves null", func(t *testing.T) {
		got := SyncOptionalString(types.StringNull(), "")
		if !got.IsNull() {
			t.Fatalf("expected null preserved, got %q", got.ValueString())
		}
	})

	t.Run("remote empty preserves unknown", func(t *testing.T) {
		got := SyncOptionalString(types.StringUnknown(), "")
		if !got.IsUnknown() {
			t.Fatalf("expected unknown preserved, got null=%v value=%q", got.IsNull(), got.ValueString())
		}
	})

	t.Run("remote whitespace-only treated as empty", func(t *testing.T) {
		got := SyncOptionalString(types.StringValue("old"), "   ")
		if !got.IsNull() {
			t.Fatalf("expected null for whitespace-only remote, got %q", got.ValueString())
		}
	})
}
