package querybuilder

import (
	"fmt"
	"strings"

	"github.com/pingcap/errors"
)

type ColumnPosition struct {
	First bool
	After *string
}

type ColumnExpressionKind string

const (
	ColumnExpressionKindDefault      ColumnExpressionKind = "DEFAULT"
	ColumnExpressionKindMaterialized ColumnExpressionKind = "MATERIALIZED"
	ColumnExpressionKindAlias        ColumnExpressionKind = "ALIAS"
	ColumnExpressionKindEphemeral    ColumnExpressionKind = "EPHEMERAL"
)

func FirstColumnPosition() *ColumnPosition {
	return &ColumnPosition{First: true}
}

func AfterColumnPosition(name string) *ColumnPosition {
	return &ColumnPosition{After: &name}
}

func BuildAlterTable(database string, name string, clusterName *string, actions []string) (string, error) {
	if err := validateRequiredField(database, "database", "ALTER TABLE"); err != nil {
		return "", err
	}
	if err := validateRequiredField(name, "name", "ALTER TABLE"); err != nil {
		return "", err
	}
	if len(actions) == 0 {
		return "", errors.New("ALTER TABLE queries require at least one action")
	}

	filtered := make([]string, 0, len(actions))
	for _, action := range actions {
		action = strings.TrimSpace(action)
		if action == "" {
			continue
		}
		filtered = append(filtered, action)
	}
	if len(filtered) == 0 {
		return "", errors.New("ALTER TABLE queries require at least one non-empty action")
	}

	tokens := []string{
		"ALTER",
		"TABLE",
		qualifiedIdentifier(database, name),
	}
	tokens = appendClusterClause(tokens, clusterName)
	// alter_sync = 2 makes a replicated ALTER return only when every replica has applied it.
	tokens = append(tokens, strings.Join(filtered, ", "), "SETTINGS alter_sync = 2")

	return strings.Join(tokens, " ") + ";", nil
}

// BuildModifyQuery builds the statement that changes the SELECT of a TO-table materialized view in place.
func BuildModifyQuery(database string, name string, query string) (string, error) {
	if err := validateRequiredField(database, "database", "ALTER TABLE"); err != nil {
		return "", err
	}
	if err := validateRequiredField(name, "name", "ALTER TABLE"); err != nil {
		return "", err
	}
	if err := validateRequiredField(query, "query", "ALTER TABLE"); err != nil {
		return "", err
	}

	return fmt.Sprintf("ALTER TABLE %s MODIFY QUERY %s;", qualifiedIdentifier(database, name), strings.TrimSpace(query)), nil
}

func BuildAddColumnAction(column ColumnDefinition, position *ColumnPosition) (string, error) {
	definition, err := buildColumnDefinition(column)
	if err != nil {
		return "", err
	}

	tokens := []string{"ADD", "COLUMN", "IF", "NOT", "EXISTS", definition}
	if positionClause := buildColumnPositionClause(position); positionClause != "" {
		tokens = append(tokens, positionClause)
	}

	return strings.Join(tokens, " "), nil
}

func BuildModifyColumnAction(column ColumnDefinition, position *ColumnPosition) (string, error) {
	definition, err := buildColumnDefinitionWithoutComment(column)
	if err != nil {
		return "", err
	}

	tokens := []string{"MODIFY", "COLUMN", definition}
	if positionClause := buildColumnPositionClause(position); positionClause != "" {
		tokens = append(tokens, positionClause)
	}

	return strings.Join(tokens, " "), nil
}

func BuildDropColumnAction(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("column name cannot be empty")
	}

	return fmt.Sprintf("DROP COLUMN IF EXISTS %s", backtick(name)), nil
}

func BuildRenameColumnAction(from string, to string) (string, error) {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if from == "" || to == "" {
		return "", errors.New("column names cannot be empty")
	}

	return fmt.Sprintf("RENAME COLUMN %s TO %s", backtick(from), backtick(to)), nil
}

func BuildCommentColumnAction(name string, comment string) (string, error) {
	name = strings.TrimSpace(name)
	comment = strings.TrimSpace(comment)
	if name == "" {
		return "", errors.New("column name cannot be empty")
	}
	if comment == "" {
		return "", errors.New("column comment cannot be empty")
	}

	return fmt.Sprintf("COMMENT COLUMN %s %s", backtick(name), quote(comment)), nil
}

func BuildRemoveColumnCommentAction(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("column name cannot be empty")
	}

	return fmt.Sprintf("MODIFY COLUMN %s REMOVE COMMENT", backtick(name)), nil
}

func BuildRemoveColumnExpressionAction(name string, kind ColumnExpressionKind) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("column name cannot be empty")
	}

	switch kind {
	case ColumnExpressionKindDefault, ColumnExpressionKindMaterialized, ColumnExpressionKindAlias:
	default:
		return "", fmt.Errorf("unsupported column expression kind: %q", kind)
	}

	return fmt.Sprintf("MODIFY COLUMN %s REMOVE %s", backtick(name), string(kind)), nil
}

// BuildRemoveColumnPropertyAction removes a CODEC or TTL from a column.
func BuildRemoveColumnPropertyAction(name string, property string) string {
	return fmt.Sprintf("MODIFY COLUMN %s REMOVE %s", backtick(strings.TrimSpace(name)), property)
}

func BuildAddIndexAction(index IndexDefinition) string {
	return "ADD " + strings.Replace(index.SQL(), "INDEX", "INDEX IF NOT EXISTS", 1)
}

func BuildDropIndexAction(name string) string {
	return fmt.Sprintf("DROP INDEX IF EXISTS %s", backtick(strings.TrimSpace(name)))
}

func BuildAddProjectionAction(projection ProjectionDefinition) string {
	return "ADD " + strings.Replace(projection.SQL(), "PROJECTION", "PROJECTION IF NOT EXISTS", 1)
}

func BuildDropProjectionAction(name string) string {
	return fmt.Sprintf("DROP PROJECTION IF EXISTS %s", backtick(strings.TrimSpace(name)))
}

func BuildAddConstraintAction(constraint ConstraintDefinition) string {
	return "ADD " + strings.Replace(constraint.SQL(), "CONSTRAINT", "CONSTRAINT IF NOT EXISTS", 1)
}

func BuildDropConstraintAction(name string) string {
	return fmt.Sprintf("DROP CONSTRAINT IF EXISTS %s", backtick(strings.TrimSpace(name)))
}

func BuildModifyOrderByAction(expr string) (string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "", errors.New("order by expression cannot be empty")
	}

	return fmt.Sprintf("MODIFY ORDER BY %s", expr), nil
}

func BuildModifySampleByAction(expr string) (string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "", errors.New("sample by expression cannot be empty")
	}

	return fmt.Sprintf("MODIFY SAMPLE BY %s", expr), nil
}

func BuildRemoveSampleByAction() string {
	return "REMOVE SAMPLE BY"
}

func BuildModifyTTLAction(expr string) (string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "", errors.New("ttl expression cannot be empty")
	}

	return fmt.Sprintf("MODIFY TTL %s", expr), nil
}

func BuildRemoveTTLAction() string {
	return "REMOVE TTL"
}

func BuildModifySettingAction(name string, value string) (string, error) {
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if name == "" {
		return "", errors.New("setting name cannot be empty")
	}
	if value == "" {
		return "", errors.New("setting value cannot be empty")
	}

	return fmt.Sprintf("MODIFY SETTING %s = %s", backtick(name), value), nil
}

func BuildResetSettingAction(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("setting name cannot be empty")
	}

	return fmt.Sprintf("RESET SETTING %s", backtick(name)), nil
}

func buildColumnDefinitionWithoutComment(column ColumnDefinition) (string, error) {
	return buildColumnDefinitionWithComment(column, false)
}

func buildColumnDefinitionWithComment(column ColumnDefinition, includeComment bool) (string, error) {
	name := strings.TrimSpace(column.Name)
	if name == "" {
		return "", errors.New("column name cannot be empty")
	}

	typeSQL, err := columnTypeSQL(column)
	if err != nil {
		return "", err
	}

	tokens := []string{
		backtick(name),
		typeSQL,
	}

	expressionCount := 0
	if !isNilOrEmpty(column.DefaultExpression) {
		expressionCount++
		tokens = append(tokens, "DEFAULT", strings.TrimSpace(*column.DefaultExpression))
	}
	if !isNilOrEmpty(column.MaterializedExpression) {
		expressionCount++
		tokens = append(tokens, "MATERIALIZED", strings.TrimSpace(*column.MaterializedExpression))
	}
	if !isNilOrEmpty(column.AliasExpression) {
		expressionCount++
		tokens = append(tokens, "ALIAS", strings.TrimSpace(*column.AliasExpression))
	}

	if !isNilOrEmpty(column.EphemeralExpression) {
		expressionCount++
		tokens = append(tokens, "EPHEMERAL", strings.TrimSpace(*column.EphemeralExpression))
	}

	if expressionCount > 1 {
		return "", errors.New("only one of default_expression, materialized_expression, alias_expression, or ephemeral_expression can be set for a column")
	}

	if includeComment && column.Comment != nil && strings.TrimSpace(*column.Comment) != "" {
		tokens = append(tokens, "COMMENT", quote(strings.TrimSpace(*column.Comment)))
	}
	if strings.TrimSpace(column.Codec) != "" {
		tokens = append(tokens, fmt.Sprintf("CODEC(%s)", strings.TrimSpace(column.Codec)))
	}
	if strings.TrimSpace(column.TTL) != "" {
		tokens = append(tokens, "TTL", strings.TrimSpace(column.TTL))
	}

	return strings.Join(tokens, " "), nil
}

func buildColumnPositionClause(position *ColumnPosition) string {
	if position == nil {
		return ""
	}
	if position.First {
		return "FIRST"
	}
	if position.After != nil && strings.TrimSpace(*position.After) != "" {
		return fmt.Sprintf("AFTER %s", backtick(strings.TrimSpace(*position.After)))
	}
	return ""
}
