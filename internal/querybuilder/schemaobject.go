package querybuilder

import (
	"fmt"
	"strings"

	"github.com/pingcap/errors"
)

type ColumnDefinition struct {
	Name                   string
	Type                   string
	Nullable               bool
	Comment                *string
	DefaultExpression      *string
	MaterializedExpression *string
	AliasExpression        *string
	EphemeralExpression    *string
	Codec                  string
	TTL                    string
}

type IndexDefinition struct {
	Name        string
	Expression  string
	Type        string
	Granularity int64
}

type ProjectionDefinition struct {
	Name     string
	Query    string
	Settings string
}

type ConstraintDefinition struct {
	Name  string
	Check string
}

type CreateTableQuery struct {
	Database    string
	Name        string
	ClusterName *string
	Columns     []ColumnDefinition
	Indexes     []IndexDefinition
	Projections []ProjectionDefinition
	Constraints []ConstraintDefinition
	Engine      string
	PartitionBy string
	OrderBy     string
	PrimaryKey  string
	SampleBy    string
	TTL         string
	Settings    string
	AsSelect    string
}

type CreateViewQuery struct {
	Database    string
	Name        string
	ClusterName *string
	OrReplace   bool
	Columns     []ColumnDefinition
	Query       string
}

type CreateMaterializedViewQuery struct {
	Database    string
	Name        string
	ClusterName *string
	Columns     []ColumnDefinition
	Engine      string
	PartitionBy string
	OrderBy     string
	PrimaryKey  string
	SampleBy    string
	TTL         string
	Settings    string
	Populate    bool
	ToTable     string
	ToColumns   []ColumnDefinition
	Query       string
}

func (q CreateTableQuery) Build() (string, error) {
	if err := validateRequiredField(q.Database, "database", "CREATE TABLE"); err != nil {
		return "", err
	}
	if err := validateRequiredField(q.Name, "name", "CREATE TABLE"); err != nil {
		return "", err
	}
	if strings.TrimSpace(q.Engine) == "" {
		return "", errors.New("engine cannot be empty for CREATE TABLE queries")
	}
	if len(q.Columns) == 0 && strings.TrimSpace(q.AsSelect) == "" {
		return "", errors.New("CREATE TABLE queries require at least one column or an as_select query")
	}

	tokens := []string{
		"CREATE",
		"TABLE",
		qualifiedIdentifier(q.Database, q.Name),
	}
	tokens = appendClusterClause(tokens, q.ClusterName)
	if len(q.Columns) > 0 {
		definitions, err := buildColumnDefinitions(q.Columns)
		if err != nil {
			return "", err
		}
		for _, index := range q.Indexes {
			definitions = append(definitions, index.SQL())
		}
		for _, projection := range q.Projections {
			definitions = append(definitions, projection.SQL())
		}
		for _, constraint := range q.Constraints {
			definitions = append(definitions, constraint.SQL())
		}
		tokens = append(tokens, fmt.Sprintf("(%s)", strings.Join(definitions, ", ")))
	}

	tokens = append(tokens, "ENGINE", "=", strings.TrimSpace(q.Engine))

	if strings.TrimSpace(q.PartitionBy) != "" {
		tokens = append(tokens, "PARTITION BY", strings.TrimSpace(q.PartitionBy))
	}
	if strings.TrimSpace(q.OrderBy) != "" {
		tokens = append(tokens, "ORDER BY", strings.TrimSpace(q.OrderBy))
	}
	if strings.TrimSpace(q.PrimaryKey) != "" {
		tokens = append(tokens, "PRIMARY KEY", strings.TrimSpace(q.PrimaryKey))
	}
	if strings.TrimSpace(q.SampleBy) != "" {
		tokens = append(tokens, "SAMPLE BY", strings.TrimSpace(q.SampleBy))
	}
	if strings.TrimSpace(q.TTL) != "" {
		tokens = append(tokens, "TTL", strings.TrimSpace(q.TTL))
	}
	if strings.TrimSpace(q.Settings) != "" {
		tokens = append(tokens, "SETTINGS", strings.TrimSpace(q.Settings))
	}
	if strings.TrimSpace(q.AsSelect) != "" {
		tokens = append(tokens, "AS", strings.TrimSpace(q.AsSelect))
	}

	return strings.Join(tokens, " ") + ";", nil
}

func (q CreateViewQuery) Build() (string, error) {
	if err := validateRequiredField(q.Database, "database", "CREATE VIEW"); err != nil {
		return "", err
	}
	if err := validateRequiredField(q.Name, "name", "CREATE VIEW"); err != nil {
		return "", err
	}
	if strings.TrimSpace(q.Query) == "" {
		return "", errors.New("query cannot be empty for CREATE VIEW queries")
	}

	tokens := []string{"CREATE"}
	if q.OrReplace {
		tokens = append(tokens, "OR REPLACE")
	}
	tokens = append(tokens, "VIEW", qualifiedIdentifier(q.Database, q.Name))
	tokens = appendClusterClause(tokens, q.ClusterName)
	if len(q.Columns) > 0 {
		signature, err := buildColumnSignatures(q.Columns)
		if err != nil {
			return "", err
		}
		tokens = append(tokens, fmt.Sprintf("(%s)", strings.Join(signature, ", ")))
	}
	tokens = append(tokens, "AS", strings.TrimSpace(q.Query))

	return strings.Join(tokens, " ") + ";", nil
}

func (q CreateMaterializedViewQuery) Build() (string, error) {
	if err := validateRequiredField(q.Database, "database", "CREATE MATERIALIZED VIEW"); err != nil {
		return "", err
	}
	if err := validateRequiredField(q.Name, "name", "CREATE MATERIALIZED VIEW"); err != nil {
		return "", err
	}
	if strings.TrimSpace(q.Query) == "" {
		return "", errors.New("query cannot be empty for CREATE MATERIALIZED VIEW queries")
	}

	hasToTable := strings.TrimSpace(q.ToTable) != ""
	hasEngine := strings.TrimSpace(q.Engine) != ""
	if hasToTable == hasEngine {
		return "", errors.New("CREATE MATERIALIZED VIEW queries require exactly one of to_table or engine")
	}
	if !hasToTable && len(q.ToColumns) > 0 {
		return "", errors.New("to_columns can only be set when to_table is set")
	}
	if hasToTable && len(q.Columns) > 0 {
		return "", errors.New("columns can only be set for engine-backed materialized views")
	}
	if hasEngine {
		if strings.TrimSpace(q.OrderBy) == "" {
			return "", errors.New("engine-backed materialized views require order_by")
		}
	}
	if hasToTable {
		if strings.TrimSpace(q.PartitionBy) != "" || strings.TrimSpace(q.OrderBy) != "" || strings.TrimSpace(q.PrimaryKey) != "" ||
			strings.TrimSpace(q.SampleBy) != "" || strings.TrimSpace(q.TTL) != "" || strings.TrimSpace(q.Settings) != "" {
			return "", errors.New("engine-backed table clauses can only be set when engine is set")
		}
	}

	tokens := []string{
		"CREATE",
		"MATERIALIZED",
		"VIEW",
		qualifiedIdentifier(q.Database, q.Name),
	}
	tokens = appendClusterClause(tokens, q.ClusterName)
	if len(q.Columns) > 0 {
		definitions, err := buildColumnDefinitions(q.Columns)
		if err != nil {
			return "", err
		}
		tokens = append(tokens, fmt.Sprintf("(%s)", strings.Join(definitions, ", ")))
	}
	if hasToTable {
		tokens = append(tokens, "TO", rawOrQualifiedIdentifier(strings.TrimSpace(q.ToTable)))
		if len(q.ToColumns) > 0 {
			signature, err := buildColumnSignatures(q.ToColumns)
			if err != nil {
				return "", err
			}
			tokens = append(tokens, fmt.Sprintf("(%s)", strings.Join(signature, ", ")))
		}
	}
	if hasEngine {
		tokens = append(tokens, "ENGINE", "=", strings.TrimSpace(q.Engine))
		if strings.TrimSpace(q.PartitionBy) != "" {
			tokens = append(tokens, "PARTITION BY", strings.TrimSpace(q.PartitionBy))
		}
		if strings.TrimSpace(q.OrderBy) != "" {
			tokens = append(tokens, "ORDER BY", strings.TrimSpace(q.OrderBy))
		}
		if strings.TrimSpace(q.PrimaryKey) != "" {
			tokens = append(tokens, "PRIMARY KEY", strings.TrimSpace(q.PrimaryKey))
		}
		if strings.TrimSpace(q.SampleBy) != "" {
			tokens = append(tokens, "SAMPLE BY", strings.TrimSpace(q.SampleBy))
		}
		if strings.TrimSpace(q.TTL) != "" {
			tokens = append(tokens, "TTL", strings.TrimSpace(q.TTL))
		}
		if strings.TrimSpace(q.Settings) != "" {
			tokens = append(tokens, "SETTINGS", strings.TrimSpace(q.Settings))
		}
	}
	if q.Populate {
		tokens = append(tokens, "POPULATE")
	}
	tokens = append(tokens, "AS", strings.TrimSpace(q.Query))

	return strings.Join(tokens, " ") + ";", nil
}

func buildColumnDefinitions(columns []ColumnDefinition) ([]string, error) {
	return buildDefinitions(columns, buildColumnDefinition)
}

func buildColumnSignatures(columns []ColumnDefinition) ([]string, error) {
	return buildDefinitions(columns, buildColumnSignature)
}

func buildColumnDefinition(column ColumnDefinition) (string, error) {
	return buildColumnDefinitionWithComment(column, true)
}

func buildColumnSignature(column ColumnDefinition) (string, error) {
	name := strings.TrimSpace(column.Name)
	if name == "" {
		return "", errors.New("column name cannot be empty")
	}

	typeSQL, err := columnTypeSQL(column)
	if err != nil {
		return "", err
	}

	return strings.Join([]string{backtick(name), typeSQL}, " "), nil
}

func columnTypeSQL(column ColumnDefinition) (string, error) {
	return typeSQL(column.Type, column.Nullable, "column")
}

func typeSQL(rawType string, nullable bool, label string) (string, error) {
	t := EffectiveType(rawType, nullable)
	if t == "" {
		return "", fmt.Errorf("%s type cannot be empty", label)
	}

	return t, nil
}

// EffectiveType returns the full type of a column: rawType wrapped in Nullable(...)
// when nullable is set and rawType is not already a Nullable type.
func EffectiveType(rawType string, nullable bool) string {
	t := strings.TrimSpace(rawType)
	if _, wrapped := UnwrapNullableType(t); nullable && !wrapped && t != "" {
		return fmt.Sprintf("Nullable(%s)", t)
	}

	return t
}

// SQL returns the index definition as it appears in CREATE TABLE and ADD INDEX.
func (d IndexDefinition) SQL() string {
	granularity := d.Granularity
	if granularity == 0 {
		granularity = 1
	}
	return fmt.Sprintf("INDEX %s %s TYPE %s GRANULARITY %d", backtick(strings.TrimSpace(d.Name)), strings.TrimSpace(d.Expression), strings.TrimSpace(d.Type), granularity)
}

// SQL returns the projection definition as it appears in CREATE TABLE and ADD PROJECTION.
func (d ProjectionDefinition) SQL() string {
	sql := fmt.Sprintf("PROJECTION %s (%s)", backtick(strings.TrimSpace(d.Name)), strings.TrimSpace(d.Query))
	if settings := strings.TrimSpace(d.Settings); settings != "" {
		sql += fmt.Sprintf(" WITH SETTINGS (%s)", settings)
	}
	return sql
}

// SQL returns the constraint definition as it appears in CREATE TABLE and ADD CONSTRAINT.
func (d ConstraintDefinition) SQL() string {
	return fmt.Sprintf("CONSTRAINT %s CHECK %s", backtick(strings.TrimSpace(d.Name)), strings.TrimSpace(d.Check))
}
