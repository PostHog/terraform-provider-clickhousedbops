package querybuilder

import (
	"fmt"
	"strings"

	"github.com/pingcap/errors"
)

type DictionaryAttributeDefinition struct {
	Name              string
	Type              string
	Nullable          bool
	DefaultExpression *string
	Expression        *string
	Hierarchical      bool
	Injective         bool
	IsObjectID        bool
}

type CreateDictionaryQuery struct {
	Database    string
	Name        string
	ClusterName *string
	OrReplace   bool
	Attributes  []DictionaryAttributeDefinition
	PrimaryKey  []string
	Source      string
	Layout      string
	Lifetime    string
	Range       string
	Settings    string
	Comment     string
}

type ShowCreateDictionaryQuery struct {
	Database string
	Name     string
}

func (q CreateDictionaryQuery) Build() (string, error) {
	if err := validateRequiredField(q.Database, "database", "CREATE DICTIONARY"); err != nil {
		return "", err
	}
	if err := validateRequiredField(q.Name, "name", "CREATE DICTIONARY"); err != nil {
		return "", err
	}
	if len(q.Attributes) == 0 {
		return "", errors.New("CREATE DICTIONARY queries require at least one attribute")
	}
	if len(q.PrimaryKey) == 0 {
		return "", errors.New("CREATE DICTIONARY queries require at least one primary key attribute")
	}
	if strings.TrimSpace(q.Source) == "" {
		return "", errors.New("source cannot be empty for CREATE DICTIONARY queries")
	}
	if strings.TrimSpace(q.Layout) == "" {
		return "", errors.New("layout cannot be empty for CREATE DICTIONARY queries")
	}
	if strings.TrimSpace(q.Lifetime) == "" {
		return "", errors.New("lifetime cannot be empty for CREATE DICTIONARY queries")
	}

	attributeDefinitions, err := buildDictionaryAttributeDefinitions(q.Attributes)
	if err != nil {
		return "", err
	}

	tokens := []string{"CREATE"}
	if q.OrReplace {
		tokens = append(tokens, "OR REPLACE")
	}
	tokens = append(tokens, "DICTIONARY", qualifiedIdentifier(q.Database, q.Name))
	tokens = appendClusterClause(tokens, q.ClusterName)

	tokens = append(tokens,
		fmt.Sprintf("(%s)", strings.Join(attributeDefinitions, ", ")),
		"PRIMARY KEY",
		strings.Join(backtickAll(q.PrimaryKey), ", "),
		fmt.Sprintf("SOURCE(%s)", strings.TrimSpace(q.Source)),
		fmt.Sprintf("LAYOUT(%s)", strings.TrimSpace(q.Layout)),
		fmt.Sprintf("LIFETIME(%s)", strings.TrimSpace(q.Lifetime)),
	)
	if strings.TrimSpace(q.Range) != "" {
		tokens = append(tokens, fmt.Sprintf("RANGE(%s)", strings.TrimSpace(q.Range)))
	}

	if strings.TrimSpace(q.Settings) != "" {
		tokens = append(tokens, "SETTINGS", strings.TrimSpace(q.Settings))
	}
	if strings.TrimSpace(q.Comment) != "" {
		tokens = append(tokens, "COMMENT", quote(strings.TrimSpace(q.Comment)))
	}

	return strings.Join(tokens, " ") + ";", nil
}

func (q ShowCreateDictionaryQuery) Build() (string, error) {
	if err := validateRequiredField(q.Database, "database", "SHOW CREATE DICTIONARY"); err != nil {
		return "", err
	}
	if err := validateRequiredField(q.Name, "name", "SHOW CREATE DICTIONARY"); err != nil {
		return "", err
	}

	return fmt.Sprintf("SHOW CREATE DICTIONARY %s;", qualifiedIdentifier(q.Database, q.Name)), nil
}

func buildDictionaryAttributeDefinitions(attributes []DictionaryAttributeDefinition) ([]string, error) {
	return buildDefinitions(attributes, buildDictionaryAttributeDefinition)
}

func buildDictionaryAttributeDefinition(attribute DictionaryAttributeDefinition) (string, error) {
	name := strings.TrimSpace(attribute.Name)
	if name == "" {
		return "", errors.New("dictionary attribute name cannot be empty")
	}

	typeSQL, err := dictionaryAttributeTypeSQL(attribute)
	if err != nil {
		return "", err
	}

	tokens := []string{
		backtick(name),
		typeSQL,
	}

	expressionCount := 0
	if !isNilOrEmpty(attribute.DefaultExpression) {
		expressionCount++
		tokens = append(tokens, "DEFAULT", strings.TrimSpace(*attribute.DefaultExpression))
	}
	if !isNilOrEmpty(attribute.Expression) {
		expressionCount++
		tokens = append(tokens, "EXPRESSION", strings.TrimSpace(*attribute.Expression))
	}
	if expressionCount > 1 {
		return "", errors.New("only one of default_expression or expression can be set for a dictionary attribute")
	}

	if attribute.Hierarchical && attribute.Injective {
		return "", errors.New("dictionary attributes cannot be both hierarchical and injective")
	}
	if attribute.Hierarchical {
		tokens = append(tokens, "HIERARCHICAL")
	}
	if attribute.Injective {
		tokens = append(tokens, "INJECTIVE")
	}
	if attribute.IsObjectID {
		tokens = append(tokens, "IS_OBJECT_ID")
	}

	return strings.Join(tokens, " "), nil
}

func dictionaryAttributeTypeSQL(attribute DictionaryAttributeDefinition) (string, error) {
	return typeSQL(attribute.Type, attribute.Nullable, "dictionary attribute")
}
