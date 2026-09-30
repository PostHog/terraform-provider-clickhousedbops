package table

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/dbops"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/querybuilder"
)

func TestValidateTableForEngineKafkaDefaults(t *testing.T) {
	defaultExpr := "1"
	diags := validateTableForEngine(
		dbops.Table{
			Engine: "Kafka('localhost:9092', 'events', 'g1', 'JSONEachRow')",
			Columns: []dbops.Column{
				{Name: "id", Type: "UInt64", DefaultExpression: &defaultExpr},
			},
		},
		dbops.TableEngineCapabilities{
			Name:             "Kafka",
			Known:            true,
			SupportsSettings: true,
		},
	)

	if !diags.HasError() {
		t.Fatal("expected Kafka default-expression validation to fail")
	}
}

func TestValidateTableForEngineMergeTreeRequiresOrderBy(t *testing.T) {
	diags := validateTableForEngine(
		dbops.Table{
			Engine: "MergeTree()",
			Columns: []dbops.Column{
				{Name: "id", Type: "UInt64"},
			},
		},
		dbops.TableEngineCapabilities{
			Name:              "MergeTree",
			Known:             true,
			SupportsSettings:  true,
			SupportsSortOrder: true,
			SupportsTTL:       true,
		},
	)

	if !diags.HasError() {
		t.Fatal("expected MergeTree validation to require ORDER BY")
	}
}

func TestPlanTableUpdateMergeTreeOrderByAppendNewColumn(t *testing.T) {
	current := dbops.Table{
		Engine:  "MergeTree()",
		OrderBy: "id",
		Columns: []dbops.Column{
			{Name: "id", Type: "UInt64"},
			{Name: "ts", Type: "DateTime"},
		},
	}
	desired := dbops.Table{
		Engine:  "MergeTree()",
		OrderBy: "(id, extra)",
		Columns: []dbops.Column{
			{Name: "id", Type: "UInt64"},
			{Name: "ts", Type: "DateTime"},
			{Name: "extra", Type: "UInt64"},
		},
	}

	plan, err := planTableUpdate(current, desired, dbops.TableEngineCapabilities{
		Name:              "MergeTree",
		Known:             true,
		SupportsSettings:  true,
		SupportsSortOrder: true,
		SupportsTTL:       true,
	}, nil)
	if err != nil {
		t.Fatalf("planTableUpdate() error = %v", err)
	}
	if len(plan.ReplaceAttrs) != 0 {
		t.Fatalf("expected in-place update, got replacement attrs %v", plan.ReplaceAttrs)
	}

	foundOrderBy := false
	for _, group := range plan.ActionGroups {
		for _, action := range group {
			if strings.Contains(action, "MODIFY ORDER BY") {
				foundOrderBy = true
			}
		}
	}
	if !foundOrderBy {
		t.Fatal("expected update plan to include MODIFY ORDER BY")
	}
}

func TestPlanTableUpdateMergeTreeReadonlySettingRequiresReplace(t *testing.T) {
	plan, err := planTableUpdate(
		dbops.Table{Engine: "MergeTree()", Settings: ""},
		dbops.Table{Engine: "MergeTree()", Settings: "index_granularity = 4096"},
		dbops.TableEngineCapabilities{Name: "MergeTree", Known: true, SupportsSettings: true, SupportsSortOrder: true, SupportsTTL: true},
		map[string]dbops.TableSettingCapability{
			"index_granularity": {Name: "index_granularity", Known: true, Readonly: true},
		},
	)
	if err != nil {
		t.Fatalf("planTableUpdate() error = %v", err)
	}
	if _, ok := plan.ReplaceAttrs["settings"]; !ok {
		t.Fatalf("expected readonly setting change to require replacement, got %v", plan.ReplaceAttrs)
	}
}

func TestPlanTableUpdateMergeTreeMutableSettingUsesAlter(t *testing.T) {
	plan, err := planTableUpdate(
		dbops.Table{Engine: "MergeTree()", Settings: ""},
		dbops.Table{Engine: "MergeTree()", Settings: "ttl_only_drop_parts = 1"},
		dbops.TableEngineCapabilities{Name: "MergeTree", Known: true, SupportsSettings: true, SupportsSortOrder: true, SupportsTTL: true},
		map[string]dbops.TableSettingCapability{
			"ttl_only_drop_parts": {Name: "ttl_only_drop_parts", Known: true, Readonly: false},
		},
	)
	if err != nil {
		t.Fatalf("planTableUpdate() error = %v", err)
	}
	if len(plan.ReplaceAttrs) != 0 {
		t.Fatalf("expected in-place settings update, got replacement attrs %v", plan.ReplaceAttrs)
	}

	foundSetting := false
	for _, group := range plan.ActionGroups {
		for _, action := range group {
			if strings.Contains(action, "MODIFY SETTING") {
				foundSetting = true
			}
		}
	}
	if !foundSetting {
		t.Fatal("expected update plan to include MODIFY SETTING")
	}
}

func TestPlanSettingsUpdateReturnsParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		current string
		desired string
		want    string
	}{
		{
			name:    "current settings",
			current: "ttl_only_drop_parts = 'unterminated",
			desired: "ttl_only_drop_parts = 1",
			want:    "unable to parse current table settings",
		},
		{
			name:    "desired settings",
			current: "ttl_only_drop_parts = 0",
			desired: "ttl_only_drop_parts = 'unterminated",
			want:    "unable to parse desired table settings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := planSettingsUpdate(tt.current, tt.desired, engineUpdateStrategy{allowSettingsAlter: true}, nil)
			if err == nil {
				t.Fatal("expected parse error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error to contain %q, got %q", tt.want, err.Error())
			}
		})
	}
}

func TestPlanTableUpdateKafkaColumnChangeRequiresReplace(t *testing.T) {
	plan, err := planTableUpdate(
		dbops.Table{Engine: "Kafka(...)", Columns: []dbops.Column{{Name: "id", Type: "UInt64"}}},
		dbops.Table{Engine: "Kafka(...)", Columns: []dbops.Column{{Name: "id", Type: "UInt64"}, {Name: "extra", Type: "String"}}},
		dbops.TableEngineCapabilities{Name: "Kafka", Known: true, SupportsSettings: true},
		nil,
	)
	if err != nil {
		t.Fatalf("planTableUpdate() error = %v", err)
	}
	if _, ok := plan.ReplaceAttrs["columns"]; !ok {
		t.Fatalf("expected Kafka column change to require replacement, got %v", plan.ReplaceAttrs)
	}
}

func TestPlanTableUpdateDistributedColumnChangeUsesAlter(t *testing.T) {
	plan, err := planTableUpdate(
		dbops.Table{Engine: "Distributed(...)", Columns: []dbops.Column{{Name: "id", Type: "UInt64"}, {Name: "ts", Type: "DateTime"}}},
		dbops.Table{Engine: "Distributed(...)", Columns: []dbops.Column{{Name: "ts", Type: "DateTime"}, {Name: "id", Type: "UInt64"}}},
		dbops.TableEngineCapabilities{Name: "Distributed", Known: true, SupportsSettings: true},
		nil,
	)
	if err != nil {
		t.Fatalf("planTableUpdate() error = %v", err)
	}
	if len(plan.ReplaceAttrs) != 0 {
		t.Fatalf("expected in-place distributed column reorder, got replacement attrs %v", plan.ReplaceAttrs)
	}
	if len(plan.ActionGroups) == 0 {
		t.Fatal("expected distributed column reorder to produce ALTER actions")
	}
}

func TestPlanTableUpdateTreatsEquivalentOrderByAsUnchanged(t *testing.T) {
	plan, err := planTableUpdate(
		dbops.Table{Engine: "MergeTree()", OrderBy: "id, ts"},
		dbops.Table{Engine: "MergeTree()", OrderBy: "(id, ts)"},
		dbops.TableEngineCapabilities{Name: "MergeTree", Known: true, SupportsSettings: true, SupportsSortOrder: true, SupportsTTL: true},
		nil,
	)
	if err != nil {
		t.Fatalf("planTableUpdate() error = %v", err)
	}
	if len(plan.ReplaceAttrs) != 0 || len(plan.ActionGroups) != 0 {
		t.Fatalf("expected equivalent ORDER BY expressions to be unchanged, got replace=%v actions=%v", plan.ReplaceAttrs, plan.ActionGroups)
	}
}

func TestPlanTableUpdateTreatsEquivalentTTLAsUnchanged(t *testing.T) {
	plan, err := planTableUpdate(
		dbops.Table{Engine: "MergeTree()", OrderBy: "tuple()", TTL: "ts + toIntervalDay(1)"},
		dbops.Table{Engine: "MergeTree()", OrderBy: "tuple()", TTL: "ts + INTERVAL 1 DAY"},
		dbops.TableEngineCapabilities{Name: "MergeTree", Known: true, SupportsSettings: true, SupportsSortOrder: true, SupportsTTL: true},
		nil,
	)
	if err != nil {
		t.Fatalf("planTableUpdate() error = %v", err)
	}
	if len(plan.ReplaceAttrs) != 0 || len(plan.ActionGroups) != 0 {
		t.Fatalf("expected equivalent TTL expressions to be unchanged, got replace=%v actions=%v", plan.ReplaceAttrs, plan.ActionGroups)
	}
}

func TestSplitTopLevelHandlesBackslashEscapedSingleQuote(t *testing.T) {
	parts, err := querybuilder.SplitTopLevel("path = 'it\\'s,ok', retries = 3", ',')
	if err != nil {
		t.Fatalf("SplitTopLevel() error = %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d: %#v", len(parts), parts)
	}
}

func TestSplitTopLevelHandlesDoubledSingleQuote(t *testing.T) {
	parts, err := querybuilder.SplitTopLevel("comment = 'team''s,blue', retries = 3", ',')
	if err != nil {
		t.Fatalf("SplitTopLevel() error = %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d: %#v", len(parts), parts)
	}
}

func TestUnwrapOuterParensIgnoresQuotedParen(t *testing.T) {
	got := unwrapOuterParens("('a)b')")
	want := "'a)b'"
	if got != want {
		t.Fatalf("unwrapOuterParens() got = %q, want %q", got, want)
	}
}

func TestExtractIdentifiersIgnoresQuotedStringsAndFunctionNames(t *testing.T) {
	identifiers := extractIdentifiers("toDate(created_at) = toDate('created_at')")

	if len(identifiers) != 1 || identifiers[0] != "created_at" {
		t.Fatalf("extractIdentifiers() got %v, want [created_at]", identifiers)
	}
}

func TestPlanTableUpdateAllowsRenameWhenOnlyQuotedStringMatchesOldName(t *testing.T) {
	current := dbops.Table{
		Engine:  "MergeTree()",
		OrderBy: "tuple()",
		Columns: []dbops.Column{
			{Name: "id", Type: "UInt64"},
			{Name: "message", Type: "String", DefaultExpression: stringPtr("'id'")},
		},
	}
	desired := dbops.Table{
		Engine:  "MergeTree()",
		OrderBy: "tuple()",
		Columns: []dbops.Column{
			{Name: "user_id", Type: "UInt64"},
			{Name: "message", Type: "String", DefaultExpression: stringPtr("'id'")},
		},
	}

	plan, err := planTableUpdate(current, desired, dbops.TableEngineCapabilities{
		Name:              "MergeTree",
		Known:             true,
		SupportsSettings:  true,
		SupportsSortOrder: true,
		SupportsTTL:       true,
	}, nil)
	if err != nil {
		t.Fatalf("planTableUpdate() error = %v", err)
	}
	if _, ok := plan.ReplaceAttrs["columns"]; ok {
		t.Fatalf("expected quoted string literal not to force replacement, got %v", plan.ReplaceAttrs)
	}
	foundRename := false
	for _, group := range plan.ActionGroups {
		for _, action := range group {
			if strings.Contains(action, "RENAME COLUMN `id` TO `user_id`") {
				foundRename = true
			}
		}
	}
	if !foundRename {
		t.Fatalf("expected rename action, got %v", plan.ActionGroups)
	}
}

func stringPtr(value string) *string {
	return &value
}

func TestPlanTableUpdateColumnPropertiesAndElements(t *testing.T) {
	mergeTree := dbops.TableEngineCapabilities{Name: "ReplicatedMergeTree", Known: true, SupportsSettings: true, SupportsSortOrder: true, SupportsTTL: true}
	ephemeral := "upper(s)"
	base := func() dbops.Table {
		return dbops.Table{
			Engine:  "ReplicatedMergeTree('/p', '{replica}')",
			OrderBy: "id",
			Columns: []dbops.Column{
				{Name: "id", Type: "UInt64"},
				{Name: "s", Type: "String", Codec: "ZSTD(1)", TTL: "ts + toIntervalDay(1)"},
				{Name: "e", Type: "String", EphemeralExpression: &ephemeral},
			},
			Indexes: []dbops.Index{
				{Name: "a", Expression: "id", Type: "minmax", Granularity: 1},
				{Name: "b", Expression: "s", Type: "bloom_filter(0.01)", Granularity: 2},
			},
			Projections: []dbops.Projection{{Name: "p", Query: "SELECT id, count() GROUP BY id"}},
			Constraints: []dbops.Constraint{{Name: "c", Check: "id > 0"}},
		}
	}

	tests := []struct {
		name        string
		change      func(table *dbops.Table)
		wantGroups  [][]string
		wantReplace string
	}{
		{
			name: "equivalent text and reordered indexes are not a change",
			change: func(table *dbops.Table) {
				table.Columns[1].Type, table.Columns[1].Nullable = "String", false
				table.Columns[1].Codec = " ZSTD( 1 ) "
				table.Indexes = []dbops.Index{{Name: "b", Expression: "s", Type: "bloom_filter( 0.01 )", Granularity: 2}, {Name: "a", Expression: "id", Type: "minmax"}}
				table.Projections[0].Query = "SELECT\n    id,\n    count()\nGROUP BY id"
			},
		},
		{
			name:       "changed codec is a MODIFY COLUMN",
			change:     func(table *dbops.Table) { table.Columns[1].Codec = "ZSTD(3)" },
			wantGroups: [][]string{{"MODIFY COLUMN `s` String CODEC(ZSTD(3)) TTL ts + toIntervalDay(1)"}},
		},
		{
			name:       "removed codec and ttl are REMOVE actions",
			change:     func(table *dbops.Table) { table.Columns[1].Codec, table.Columns[1].TTL = "", "" },
			wantGroups: [][]string{{"MODIFY COLUMN `s` REMOVE CODEC", "MODIFY COLUMN `s` REMOVE TTL"}},
		},
		{
			name:        "ephemeral cannot be removed in place",
			change:      func(table *dbops.Table) { table.Columns[2].EphemeralExpression = nil },
			wantReplace: "columns",
		},
		{
			name: "changed elements are dropped first and added after the columns",
			change: func(table *dbops.Table) {
				table.Columns = append(table.Columns, dbops.Column{Name: "n", Type: "UInt8"})
				table.Indexes = []dbops.Index{{Name: "a", Expression: "id", Type: "set(10)", Granularity: 1}, {Name: "n_idx", Expression: "n", Type: "minmax"}}
				table.Projections = nil
				table.Constraints[0].Check = "id > 1"
			},
			wantGroups: [][]string{
				{"DROP INDEX IF EXISTS `a`", "DROP INDEX IF EXISTS `b`", "DROP PROJECTION IF EXISTS `p`", "DROP CONSTRAINT IF EXISTS `c`"},
				{"ADD COLUMN IF NOT EXISTS `n` UInt8 AFTER `e`"},
				{"ADD INDEX IF NOT EXISTS `a` id TYPE set(10) GRANULARITY 1", "ADD INDEX IF NOT EXISTS `n_idx` n TYPE minmax GRANULARITY 1", "ADD CONSTRAINT IF NOT EXISTS `c` CHECK id > 1"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired := base()
			tt.change(&desired)

			plan, err := planTableUpdate(base(), desired, mergeTree, nil)
			if err != nil {
				t.Fatalf("planTableUpdate() error = %v", err)
			}
			if tt.wantReplace != "" {
				if _, ok := plan.ReplaceAttrs[tt.wantReplace]; !ok {
					t.Fatalf("expected %s to require replacement, got %#v", tt.wantReplace, plan)
				}
				return
			}
			if len(plan.ReplaceAttrs) > 0 {
				t.Fatalf("unexpected replacement: %#v", plan.ReplaceAttrs)
			}
			if len(plan.ActionGroups) != len(tt.wantGroups) {
				t.Fatalf("action groups = %#v, want %#v", plan.ActionGroups, tt.wantGroups)
			}
			for i, want := range tt.wantGroups {
				if strings.Join(plan.ActionGroups[i], "; ") != strings.Join(want, "; ") {
					t.Errorf("group %d = %#v, want %#v", i, plan.ActionGroups[i], want)
				}
			}
		})
	}
}

func TestPlanTableUpdateElementsRequireReplaceOutsideMergeTree(t *testing.T) {
	current := dbops.Table{Engine: "Kafka", Columns: []dbops.Column{{Name: "id", Type: "UInt64"}}}
	desired := current
	desired.Indexes = []dbops.Index{{Name: "a", Expression: "id", Type: "minmax"}}

	plan, err := planTableUpdate(current, desired, dbops.TableEngineCapabilities{Name: "Kafka", Known: true, SupportsSettings: true}, nil)
	if err != nil {
		t.Fatalf("planTableUpdate() error = %v", err)
	}
	if _, ok := plan.ReplaceAttrs["indexes"]; !ok {
		t.Fatalf("expected indexes to require replacement, got %#v", plan)
	}
}

func TestPlanSettingsUpdateGroupsActions(t *testing.T) {
	capabilities := map[string]dbops.TableSettingCapability{
		"index_granularity":       {Name: "index_granularity", Known: true, Readonly: true, Default: "8192"},
		"storage_policy":          {Name: "storage_policy", Known: true, Readonly: true, Default: "default"},
		"ttl_only_drop_parts":     {Name: "ttl_only_drop_parts", Known: true},
		"merge_with_ttl_timeout":  {Name: "merge_with_ttl_timeout", Known: true},
		"old_parts_lifetime":      {Name: "old_parts_lifetime", Known: true},
		"max_suspicious_broken_p": {Name: "max_suspicious_broken_p", Known: true},
	}
	strategy := engineUpdateStrategy{allowSettingsAlter: true}

	tests := []struct {
		name        string
		current     string
		desired     string
		wantGroups  [][]string
		wantReplace bool
	}{
		{
			name:    "several settings are one MODIFY SETTING and one RESET SETTING statement",
			current: "index_granularity = 8192, old_parts_lifetime = 1, max_suspicious_broken_p = 2",
			desired: "ttl_only_drop_parts = 1, merge_with_ttl_timeout = 3600",
			wantGroups: [][]string{
				{"MODIFY SETTING `ttl_only_drop_parts` = 1, `merge_with_ttl_timeout` = 3600"},
				{"RESET SETTING `old_parts_lifetime`, `max_suspicious_broken_p`"},
			},
		},
		{
			name:    "an undeclared read-only setting at the server default is not a change",
			current: "index_granularity = 8192, storage_policy = 'default', ttl_only_drop_parts = 1",
			desired: "ttl_only_drop_parts = 1",
		},
		{
			name:        "an undeclared read-only setting with another value requires replacement",
			current:     "index_granularity = 4096, ttl_only_drop_parts = 1",
			desired:     "ttl_only_drop_parts = 1",
			wantReplace: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups, replace, err := planSettingsUpdate(tt.current, tt.desired, strategy, capabilities)
			if err != nil {
				t.Fatalf("planSettingsUpdate() error = %v", err)
			}
			if replace != tt.wantReplace || len(groups) != len(tt.wantGroups) {
				t.Fatalf("planSettingsUpdate() = %#v, %v; want %#v, %v", groups, replace, tt.wantGroups, tt.wantReplace)
			}
			for i, want := range tt.wantGroups {
				if strings.Join(groups[i], "; ") != strings.Join(want, "; ") {
					t.Errorf("group %d = %#v, want %#v", i, groups[i], want)
				}
			}
		})
	}
}

func TestFilterUnmanaged(t *testing.T) {
	remote := dbops.Table{
		Columns: []dbops.Column{{Name: "id"}, {Name: "mat_a"}, {Name: "mat_declared"}, {Name: "other"}},
		Indexes: []dbops.Index{{Name: "idx"}, {Name: "mat_a_idx"}, {Name: "mat_declared_idx"}},
	}
	declared := dbops.Table{
		Columns: []dbops.Column{{Name: "id"}, {Name: "mat_declared"}},
		Indexes: []dbops.Index{{Name: "mat_declared_idx"}},
	}
	patterns := []*regexp.Regexp{regexp.MustCompile("^mat_")}

	filtered := filterUnmanaged(remote, declared, patterns, patterns)

	names := make([]string, 0)
	for _, column := range filtered.Columns {
		names = append(names, column.Name)
	}
	for _, index := range filtered.Indexes {
		names = append(names, index.Name)
	}
	if got := strings.Join(names, ","); got != "id,mat_declared,other,idx,mat_declared_idx" {
		t.Fatalf("filterUnmanaged() kept %s", got)
	}
	if len(remote.Columns) != 4 || len(remote.Indexes) != 3 {
		t.Fatal("filterUnmanaged() must not change the remote table")
	}
}
