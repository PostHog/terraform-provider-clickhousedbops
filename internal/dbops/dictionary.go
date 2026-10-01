package dbops

import (
	"context"
	"strings"

	"github.com/pingcap/errors"

	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/clickhouseclient"
	"github.com/ClickHouse/terraform-provider-clickhousedbops/internal/querybuilder"
)

type DictionaryAttribute struct {
	Name              string
	Type              string
	Nullable          bool
	DefaultExpression *string
	Expression        *string
	Hierarchical      bool
	Injective         bool
	IsObjectID        bool
}

type Dictionary struct {
	Database        string
	Name            string
	Comment         string
	Attributes      []DictionaryAttribute
	PrimaryKey      []string
	Source          string
	Layout          string
	Lifetime        string
	Range           string
	Settings        string
	CreateStatement string
}

func (i *impl) CreateDictionary(ctx context.Context, dictionary Dictionary, clusterName *string) (*Dictionary, error) {
	return i.createDictionary(ctx, dictionary, clusterName, false)
}

func (i *impl) ReplaceDictionary(ctx context.Context, dictionary Dictionary, clusterName *string) (*Dictionary, error) {
	return i.createDictionary(ctx, dictionary, clusterName, true)
}

func (i *impl) createDictionary(ctx context.Context, dictionary Dictionary, clusterName *string, orReplace bool) (*Dictionary, error) {
	sql, err := querybuilder.CreateDictionaryQuery{
		Database:    dictionary.Database,
		Name:        dictionary.Name,
		ClusterName: clusterName,
		OrReplace:   orReplace,
		Attributes:  toQueryBuilderDictionaryAttributes(dictionary.Attributes),
		PrimaryKey:  dictionary.PrimaryKey,
		Source:      dictionary.Source,
		Layout:      dictionary.Layout,
		Lifetime:    dictionary.Lifetime,
		Range:       dictionary.Range,
		Settings:    dictionary.Settings,
		Comment:     dictionary.Comment,
	}.Build()
	if err != nil {
		return nil, errors.WithMessage(err, "error building query")
	}

	if err := i.clickhouseClient.Exec(ctx, sql); err != nil {
		return nil, errors.WithMessage(err, "error running query")
	}

	return i.GetDictionary(ctx, dictionary.Database, dictionary.Name, clusterName)
}

func (i *impl) GetDictionary(ctx context.Context, database string, name string, clusterName *string) (*Dictionary, error) {
	sql, err := querybuilder.NewSelect(
		[]querybuilder.Field{
			querybuilder.NewField("database"),
			querybuilder.NewField("name"),
			querybuilder.NewField("comment"),
		},
		"system.dictionaries",
	).WithCluster(clusterName).Where(
		querybuilder.WhereEquals("database", database),
		querybuilder.WhereEquals("name", name),
	).Build()
	if err != nil {
		return nil, errors.WithMessage(err, "error building query")
	}

	var dictionary *Dictionary
	err = i.clickhouseClient.Select(ctx, sql, func(data clickhouseclient.Row) error {
		if dictionary != nil {
			return nil
		}

		dbName, err := data.GetString("database")
		if err != nil {
			return errors.WithMessage(err, "error scanning query result, missing 'database' field")
		}
		dictName, err := data.GetString("name")
		if err != nil {
			return errors.WithMessage(err, "error scanning query result, missing 'name' field")
		}
		comment, err := data.GetString("comment")
		if err != nil {
			return errors.WithMessage(err, "error scanning query result, missing 'comment' field")
		}

		dictionary = &Dictionary{
			Database: dbName,
			Name:     dictName,
			Comment:  comment,
		}
		return nil
	})
	if err != nil {
		return nil, errors.WithMessage(err, "error running query")
	}

	if dictionary == nil {
		return nil, nil
	}

	showCreateSQL, err := querybuilder.ShowCreateDictionaryQuery{Database: database, Name: name}.Build()
	if err != nil {
		return nil, errors.WithMessage(err, "error building query")
	}
	showCreateSQL = i.withSecretsShown(showCreateSQL)

	err = i.clickhouseClient.Select(ctx, showCreateSQL, func(data clickhouseclient.Row) error {
		statement, err := data.GetString("statement")
		if err != nil {
			return errors.WithMessage(err, "error scanning query result, missing 'statement' field")
		}
		dictionary.CreateStatement = statement
		return nil
	})
	if err != nil {
		return nil, errors.WithMessage(err, "error running SHOW CREATE DICTIONARY")
	}
	if i.manageDictPasswords && strings.Contains(dictionary.CreateStatement, "'[HIDDEN]'") {
		return nil, errors.Errorf("ClickHouse hid the password of %s.%s although manage_dictionary_passwords is set: the server needs display_secrets_in_show_and_select = 1 in its config, and the provider's user the displaySecretsInShowAndSelect privilege", database, name)
	}

	if dictionary.CreateStatement == "" {
		return nil, errors.Errorf("failed retrieving SHOW CREATE DICTIONARY for %s.%s", database, name)
	}

	definition, err := parseCreateDictionaryDefinition(dictionary.CreateStatement)
	if err != nil {
		return nil, errors.WithMessage(err, "error parsing CREATE DICTIONARY statement")
	}

	dictionary.Attributes = definition.Attributes
	dictionary.PrimaryKey = definition.PrimaryKey
	dictionary.Source = definition.Source
	dictionary.Layout = definition.Layout
	dictionary.Lifetime = definition.Lifetime
	dictionary.Range = definition.Range
	if definition.Settings != "" {
		dictionary.Settings = definition.Settings
	}

	return dictionary, nil
}

func (i *impl) DeleteDictionary(ctx context.Context, database string, name string, clusterName *string) error {
	dictionary, err := i.GetDictionary(ctx, database, name, clusterName)
	if err != nil {
		return err
	}
	return i.deleteIfExists(ctx, dictionary != nil, querybuilder.NewDropDictionary(database, name).WithCluster(clusterName))
}

func toQueryBuilderDictionaryAttributes(attributes []DictionaryAttribute) []querybuilder.DictionaryAttributeDefinition {
	if len(attributes) == 0 {
		return nil
	}

	ret := make([]querybuilder.DictionaryAttributeDefinition, 0, len(attributes))
	for _, attribute := range attributes {
		ret = append(ret, querybuilder.DictionaryAttributeDefinition{
			Name:              attribute.Name,
			Type:              attribute.Type,
			Nullable:          attribute.Nullable,
			DefaultExpression: attribute.DefaultExpression,
			Expression:        attribute.Expression,
			Hierarchical:      attribute.Hierarchical,
			Injective:         attribute.Injective,
			IsObjectID:        attribute.IsObjectID,
		})
	}

	return ret
}

type createDictionaryDefinition struct {
	Attributes []DictionaryAttribute
	PrimaryKey []string
	Source     string
	Layout     string
	Lifetime   string
	Range      string
	Settings   string
}

func parseCreateDictionaryDefinition(createStatement string) (createDictionaryDefinition, error) {
	definition := createDictionaryDefinition{}
	statement := strings.TrimSpace(strings.TrimSuffix(createStatement, ";"))
	if statement == "" {
		return definition, nil
	}

	// Extract attributes from the parenthesized list after the dictionary identifier
	openIdx, closeIdx, ok, err := findTrailingDictionaryColumns(statement)
	if err != nil {
		return definition, err
	}
	if ok {
		attributes, err := parseDictionaryAttributes(statement[openIdx+1 : closeIdx])
		if err != nil {
			return definition, err
		}
		definition.Attributes = attributes
	}

	// Parse clauses after the column list
	remainder := statement
	if ok {
		remainder = strings.TrimSpace(statement[closeIdx+1:])
	}

	// PRIMARY KEY
	pkIndex, err := querybuilder.FindTopLevelKeyword(remainder, "PRIMARY KEY", 0)
	if err != nil {
		return definition, err
	}
	if pkIndex != -1 {
		pkValue := remainder[pkIndex+len("PRIMARY KEY"):]
		// PRIMARY KEY value ends at SOURCE
		srcIndex, err := querybuilder.FindTopLevelKeyword(pkValue, "SOURCE", 0)
		if err != nil {
			return definition, err
		}
		if srcIndex != -1 {
			pkValue = pkValue[:srcIndex]
		}
		pk, err := parsePrimaryKeyList(strings.TrimSpace(pkValue))
		if err != nil {
			return definition, err
		}
		definition.PrimaryKey = pk
	}

	// SOURCE(...) — extract inner content
	srcIndex, err := querybuilder.FindTopLevelKeyword(remainder, "SOURCE", 0)
	if err != nil {
		return definition, err
	}
	if srcIndex != -1 {
		inner, end, err := extractTopLevelParenContent(remainder, srcIndex+len("SOURCE"))
		if err != nil {
			return definition, errors.WithMessage(err, "error parsing SOURCE clause")
		}
		definition.Source = inner
		remainder = remainder[end:]
	}

	// LIFETIME(...) — extract inner content
	ltIndex, err := querybuilder.FindTopLevelKeyword(remainder, "LIFETIME", 0)
	if err != nil {
		return definition, err
	}
	if ltIndex != -1 {
		inner, end, err := extractTopLevelParenContent(remainder, ltIndex+len("LIFETIME"))
		if err != nil {
			return definition, errors.WithMessage(err, "error parsing LIFETIME clause")
		}
		definition.Lifetime = inner
		remainder = remainder[end:]
	}

	// LAYOUT(...) — extract inner content
	layIndex, err := querybuilder.FindTopLevelKeyword(remainder, "LAYOUT", 0)
	if err != nil {
		return definition, err
	}
	if layIndex != -1 {
		inner, end, err := extractTopLevelParenContent(remainder, layIndex+len("LAYOUT"))
		if err != nil {
			return definition, errors.WithMessage(err, "error parsing LAYOUT clause")
		}
		definition.Layout = inner
		remainder = remainder[end:]
	}

	// RANGE(...) — extract inner content
	rangeIndex, err := querybuilder.FindTopLevelKeyword(remainder, "RANGE", 0)
	if err != nil {
		return definition, err
	}
	if rangeIndex != -1 {
		inner, end, err := extractTopLevelParenContent(remainder, rangeIndex+len("RANGE"))
		if err != nil {
			return definition, errors.WithMessage(err, "error parsing RANGE clause")
		}
		definition.Range = inner
		remainder = remainder[end:]
	}

	// SETTINGS
	setIndex, err := querybuilder.FindTopLevelKeyword(remainder, "SETTINGS", 0)
	if err != nil {
		return definition, err
	}
	if setIndex != -1 {
		settingsValue := strings.TrimSpace(remainder[setIndex+len("SETTINGS"):])
		// Settings end at COMMENT or end of string
		commentIndex, err := querybuilder.FindTopLevelKeyword(settingsValue, "COMMENT", 0)
		if err != nil {
			return definition, err
		}
		if commentIndex != -1 {
			settingsValue = strings.TrimSpace(settingsValue[:commentIndex])
		}
		definition.Settings = settingsValue
	}

	return definition, nil
}

// findTrailingDictionaryColumns finds the first top-level parenthesized group
// in the CREATE DICTIONARY statement — this is the attribute list.
func findTrailingDictionaryColumns(statement string) (int, int, bool, error) {
	// Find PRIMARY KEY to limit our search
	pkIndex, err := querybuilder.FindTopLevelKeyword(statement, "PRIMARY KEY", 0)
	if err != nil {
		return 0, 0, false, err
	}
	searchIn := statement
	if pkIndex != -1 {
		searchIn = statement[:pkIndex]
	}

	return querybuilder.FindTrailingTopLevelParentheses(searchIn)
}

// extractTopLevelParenContent extracts the content between balanced parentheses
// starting the search from startIdx. Returns inner content and end position.
func extractTopLevelParenContent(raw string, startIdx int) (string, int, error) {
	openIdx := strings.IndexByte(raw[startIdx:], '(')
	if openIdx == -1 {
		return "", startIdx, errors.New("expected '(' not found")
	}
	openIdx += startIdx

	closeIdx, err := querybuilder.FindMatchingClose(raw, openIdx)
	if err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(raw[openIdx+1 : closeIdx]), closeIdx + 1, nil
}

// parseDictionaryAttributes parses the comma-separated attribute definitions.
func parseDictionaryAttributes(raw string) ([]DictionaryAttribute, error) {
	parts, err := querybuilder.SplitTopLevelCSV(raw)
	if err != nil {
		return nil, err
	}

	attributes := make([]DictionaryAttribute, 0, len(parts))
	for _, part := range parts {
		attr, err := parseDictionaryAttribute(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		attributes = append(attributes, attr)
	}
	return attributes, nil
}

// parseDictionaryAttribute parses a single dictionary attribute definition.
// Format: name Type [DEFAULT expr] [EXPRESSION expr] [HIERARCHICAL] [INJECTIVE] [IS_OBJECT_ID]
func parseDictionaryAttribute(raw string) (DictionaryAttribute, error) {
	attr := DictionaryAttribute{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return attr, errors.New("empty dictionary attribute definition")
	}

	// Extract name
	nameEnd, err := querybuilder.FindColumnNameEnd(raw)
	if err != nil {
		return attr, err
	}
	attr.Name = querybuilder.UnquoteIdentifier(raw[:nameEnd])
	remainder := strings.TrimSpace(raw[nameEnd:])

	// Extract type — everything up to the first keyword or end
	typEnd := len(remainder)
	for _, kw := range []string{"DEFAULT", "EXPRESSION", "HIERARCHICAL", "INJECTIVE", "IS_OBJECT_ID"} {
		idx, err := querybuilder.FindTopLevelKeyword(remainder, kw, 0)
		if err != nil {
			return attr, err
		}
		if idx != -1 && idx < typEnd {
			typEnd = idx
		}
	}

	attr.Type = strings.TrimSpace(remainder[:typEnd])

	remainder = strings.TrimSpace(remainder[typEnd:])

	// Parse modifier keywords using SQL-aware keyword matching
	for remainder != "" {
		matched := false
		for _, kw := range []string{"DEFAULT", "EXPRESSION", "HIERARCHICAL", "INJECTIVE", "IS_OBJECT_ID"} {
			idx, err := querybuilder.FindTopLevelKeyword(remainder, kw, 0)
			if err != nil {
				return attr, err
			}
			if idx != 0 {
				continue
			}
			matched = true
			switch kw {
			case "DEFAULT":
				remainder = strings.TrimSpace(remainder[len("DEFAULT"):])
				expr, rest, err := extractExpressionUntilKeyword(remainder)
				if err != nil {
					return attr, err
				}
				attr.DefaultExpression = &expr
				remainder = rest
			case "EXPRESSION":
				remainder = strings.TrimSpace(remainder[len("EXPRESSION"):])
				expr, rest, err := extractExpressionUntilKeyword(remainder)
				if err != nil {
					return attr, err
				}
				attr.Expression = &expr
				remainder = rest
			case "HIERARCHICAL":
				attr.Hierarchical = true
				remainder = strings.TrimSpace(remainder[len("HIERARCHICAL"):])
			case "INJECTIVE":
				attr.Injective = true
				remainder = strings.TrimSpace(remainder[len("INJECTIVE"):])
			case "IS_OBJECT_ID":
				attr.IsObjectID = true
				remainder = strings.TrimSpace(remainder[len("IS_OBJECT_ID"):])
			}
			break
		}
		if !matched {
			break
		}
	}

	return attr, nil
}

// extractExpressionUntilKeyword extracts an expression value that ends before
// the next dictionary attribute keyword or end of string.
func extractExpressionUntilKeyword(raw string) (string, string, error) {
	keywords := []string{"DEFAULT", "EXPRESSION", "HIERARCHICAL", "INJECTIVE", "IS_OBJECT_ID"}
	minIdx := len(raw)
	for _, kw := range keywords {
		idx, err := querybuilder.FindTopLevelKeyword(raw, kw, 0)
		if err != nil {
			return "", "", errors.WithMessage(err, "error scanning dictionary attribute expression")
		}
		if idx != -1 && idx < minIdx {
			minIdx = idx
		}
	}
	return strings.TrimSpace(raw[:minIdx]), strings.TrimSpace(raw[minIdx:]), nil
}

// parsePrimaryKeyList parses a PRIMARY KEY value which may be a single identifier
// or a parenthesized comma-separated list.
func parsePrimaryKeyList(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	// Strip outer parens using balanced-paren detection
	if strings.HasPrefix(raw, "(") {
		closeIdx, err := querybuilder.FindMatchingClose(raw, 0)
		if err != nil {
			return nil, errors.WithMessage(err, "error parsing PRIMARY KEY list")
		}
		if closeIdx == len(raw)-1 {
			raw = raw[1:closeIdx]
		}
	}
	parts, err := querybuilder.SplitTopLevelCSV(raw)
	if err != nil {
		return nil, errors.WithMessage(err, "error splitting PRIMARY KEY list")
	}
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, querybuilder.UnquoteIdentifier(p))
		}
	}
	return result, nil
}
