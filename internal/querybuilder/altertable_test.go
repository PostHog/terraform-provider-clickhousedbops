package querybuilder

import "testing"

func Test_BuildModifyColumnAction(t *testing.T) {
	defaultExpr := "now()"
	materializedExpr := "toDate(created_at)"
	afterCol := "event"

	tests := []struct {
		name     string
		column   ColumnDefinition
		position *ColumnPosition
		want     string
		wantErr  bool
	}{
		{
			name:   "Type change only",
			column: ColumnDefinition{Name: "col1", Type: "String"},
			want:   "MODIFY COLUMN `col1` String",
		},
		{
			name:   "Nullable change",
			column: ColumnDefinition{Name: "col1", Type: "String", Nullable: true},
			want:   "MODIFY COLUMN `col1` Nullable(String)",
		},
		{
			name:   "With default expression",
			column: ColumnDefinition{Name: "created_at", Type: "DateTime", DefaultExpression: &defaultExpr},
			want:   "MODIFY COLUMN `created_at` DateTime DEFAULT now()",
		},
		{
			name:   "With materialized expression",
			column: ColumnDefinition{Name: "event_date", Type: "Date", MaterializedExpression: &materializedExpr},
			want:   "MODIFY COLUMN `event_date` Date MATERIALIZED toDate(created_at)",
		},
		{
			name:     "With FIRST position",
			column:   ColumnDefinition{Name: "col1", Type: "UInt64"},
			position: FirstColumnPosition(),
			want:     "MODIFY COLUMN `col1` UInt64 FIRST",
		},
		{
			name:     "With AFTER position",
			column:   ColumnDefinition{Name: "col1", Type: "UInt64"},
			position: AfterColumnPosition(afterCol),
			want:     "MODIFY COLUMN `col1` UInt64 AFTER `event`",
		},
		{
			name:    "Empty column name",
			column:  ColumnDefinition{Name: "", Type: "String"},
			wantErr: true,
		},
		{
			name:    "Empty type",
			column:  ColumnDefinition{Name: "col1", Type: ""},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildModifyColumnAction(tt.column, tt.position)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildModifyColumnAction() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("BuildModifyColumnAction() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_BuildModifyColumnAction_ExcludesComment(t *testing.T) {
	comment := "this should not appear"
	got, err := BuildModifyColumnAction(
		ColumnDefinition{Name: "col1", Type: "String", Comment: &comment},
		nil,
	)
	if err != nil {
		t.Fatalf("BuildModifyColumnAction() error = %v", err)
	}

	want := "MODIFY COLUMN `col1` String"
	if got != want {
		t.Fatalf("BuildModifyColumnAction() got = %v, want %v", got, want)
	}
}

func Test_BuildDropColumnAction(t *testing.T) {
	tests := []struct {
		name    string
		column  string
		want    string
		wantErr bool
	}{
		{
			name:   "Basic drop",
			column: "col1",
			want:   "DROP COLUMN IF EXISTS `col1`",
		},
		{
			name:    "Empty name",
			column:  "",
			wantErr: true,
		},
		{
			name:    "Whitespace only name",
			column:  "   ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildDropColumnAction(tt.column)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildDropColumnAction() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("BuildDropColumnAction() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_BuildRenameColumnAction(t *testing.T) {
	tests := []struct {
		name    string
		from    string
		to      string
		want    string
		wantErr bool
	}{
		{
			name: "Basic rename",
			from: "old_name",
			to:   "new_name",
			want: "RENAME COLUMN `old_name` TO `new_name`",
		},
		{
			name:    "Empty from",
			from:    "",
			to:      "new_name",
			wantErr: true,
		},
		{
			name:    "Empty to",
			from:    "old_name",
			to:      "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildRenameColumnAction(tt.from, tt.to)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildRenameColumnAction() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("BuildRenameColumnAction() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_BuildCommentColumnAction(t *testing.T) {
	tests := []struct {
		name    string
		column  string
		comment string
		want    string
		wantErr bool
	}{
		{
			name:    "Set comment",
			column:  "col1",
			comment: "my comment",
			want:    "COMMENT COLUMN `col1` 'my comment'",
		},
		{
			name:    "Empty column name",
			column:  "",
			comment: "my comment",
			wantErr: true,
		},
		{
			name:    "Empty comment",
			column:  "col1",
			comment: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildCommentColumnAction(tt.column, tt.comment)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildCommentColumnAction() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("BuildCommentColumnAction() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_BuildRemoveColumnCommentAction(t *testing.T) {
	tests := []struct {
		name    string
		column  string
		want    string
		wantErr bool
	}{
		{
			name:   "Remove comment",
			column: "col1",
			want:   "MODIFY COLUMN `col1` REMOVE COMMENT",
		},
		{
			name:    "Empty column name",
			column:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildRemoveColumnCommentAction(tt.column)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildRemoveColumnCommentAction() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("BuildRemoveColumnCommentAction() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_BuildRemoveColumnExpressionAction(t *testing.T) {
	tests := []struct {
		name    string
		column  string
		kind    ColumnExpressionKind
		want    string
		wantErr bool
	}{
		{
			name:   "Remove DEFAULT expression",
			column: "col1",
			kind:   ColumnExpressionKindDefault,
			want:   "MODIFY COLUMN `col1` REMOVE DEFAULT",
		},
		{
			name:   "Remove MATERIALIZED expression",
			column: "col1",
			kind:   ColumnExpressionKindMaterialized,
			want:   "MODIFY COLUMN `col1` REMOVE MATERIALIZED",
		},
		{
			name:   "Remove ALIAS expression",
			column: "col1",
			kind:   ColumnExpressionKindAlias,
			want:   "MODIFY COLUMN `col1` REMOVE ALIAS",
		},
		{
			name:    "Invalid kind",
			column:  "col1",
			kind:    ColumnExpressionKind("BOGUS"),
			wantErr: true,
		},
		{
			name:    "Empty column name",
			column:  "",
			kind:    ColumnExpressionKindDefault,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildRemoveColumnExpressionAction(tt.column, tt.kind)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildRemoveColumnExpressionAction() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("BuildRemoveColumnExpressionAction() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_BuildModifySampleByAction(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		want    string
		wantErr bool
	}{
		{
			name: "Change sample by",
			expr: "intHash32(user_id)",
			want: "MODIFY SAMPLE BY intHash32(user_id)",
		},
		{
			name:    "Empty expression",
			expr:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildModifySampleByAction(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildModifySampleByAction() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("BuildModifySampleByAction() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_BuildRemoveSampleByAction(t *testing.T) {
	got := BuildRemoveSampleByAction()
	want := "REMOVE SAMPLE BY"
	if got != want {
		t.Fatalf("BuildRemoveSampleByAction() got = %v, want %v", got, want)
	}
}

func Test_BuildModifyTTLAction(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		want    string
		wantErr bool
	}{
		{
			name: "Change TTL",
			expr: "created_at + INTERVAL 1 MONTH",
			want: "MODIFY TTL created_at + INTERVAL 1 MONTH",
		},
		{
			name:    "Empty expression",
			expr:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildModifyTTLAction(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildModifyTTLAction() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("BuildModifyTTLAction() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_BuildRemoveTTLAction(t *testing.T) {
	got := BuildRemoveTTLAction()
	want := "REMOVE TTL"
	if got != want {
		t.Fatalf("BuildRemoveTTLAction() got = %v, want %v", got, want)
	}
}

func Test_BuildResetSettingAction(t *testing.T) {
	tests := []struct {
		name    string
		setting string
		want    string
		wantErr bool
	}{
		{
			name:    "Reset setting",
			setting: "ttl_only_drop_parts",
			want:    "RESET SETTING `ttl_only_drop_parts`",
		},
		{
			name:    "Empty name",
			setting: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildResetSettingAction(tt.setting)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildResetSettingAction() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("BuildResetSettingAction() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_BuildModifySettingAction(t *testing.T) {
	tests := []struct {
		name    string
		setting string
		value   string
		want    string
		wantErr bool
	}{
		{
			name:    "Change setting",
			setting: "ttl_only_drop_parts",
			value:   "1",
			want:    "MODIFY SETTING `ttl_only_drop_parts` = 1",
		},
		{
			name:    "Empty name",
			setting: "",
			value:   "1",
			wantErr: true,
		},
		{
			name:    "Empty value",
			setting: "ttl_only_drop_parts",
			value:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildModifySettingAction(tt.setting, tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildModifySettingAction() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("BuildModifySettingAction() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_BuildAlterTable_EmptyActions(t *testing.T) {
	_, err := BuildAlterTable("db1", "tbl1", nil, []string{})
	if err == nil {
		t.Fatal("expected error for empty actions")
	}
}

func Test_BuildAlterTable_AllWhitespaceActions(t *testing.T) {
	_, err := BuildAlterTable("db1", "tbl1", nil, []string{"  ", ""})
	if err == nil {
		t.Fatal("expected error when all actions are whitespace")
	}
}

func Test_BuildAlterTable_ValidActions(t *testing.T) {
	cluster := "cluster1"

	tests := []struct {
		name    string
		db      string
		table   string
		cluster *string
		actions []string
		want    string
		wantErr bool
	}{
		{
			name:    "Single action without cluster",
			db:      "db1",
			table:   "tbl1",
			actions: []string{"DROP COLUMN `col1`"},
			want:    "ALTER TABLE `db1`.`tbl1` DROP COLUMN `col1` SETTINGS alter_sync = 2;",
		},
		{
			name:    "Multiple actions with cluster",
			db:      "db1",
			table:   "tbl1",
			cluster: &cluster,
			actions: []string{"DROP COLUMN `col1`", "DROP COLUMN `col2`"},
			want:    "ALTER TABLE `db1`.`tbl1` ON CLUSTER 'cluster1' DROP COLUMN `col1`, DROP COLUMN `col2` SETTINGS alter_sync = 2;",
		},
		{
			name:    "Empty database",
			db:      "",
			table:   "tbl1",
			actions: []string{"DROP COLUMN `col1`"},
			wantErr: true,
		},
		{
			name:    "Empty table name",
			db:      "db1",
			table:   "",
			actions: []string{"DROP COLUMN `col1`"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildAlterTable(tt.db, tt.table, tt.cluster, tt.actions)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildAlterTable() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("BuildAlterTable() got = %v, want %v", got, tt.want)
			}
		})
	}
}
