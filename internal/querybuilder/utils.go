package querybuilder

import (
	"errors"
	"fmt"
	"strings"
)

// backtick escapes the ` characted in strings to make them safe for use in SQL queries as literal values.
func backtick(s string) string {
	return fmt.Sprintf("`%s`", strings.ReplaceAll(backslash(s), "`", "\\`"))
}

func backtickAll(s []string) []string {
	if s == nil {
		return nil
	}
	ret := make([]string, 0)
	for _, p := range s {
		ret = append(ret, backtick(p))
	}
	return ret
}

func quote(s string) string {
	return fmt.Sprintf("'%s'", strings.ReplaceAll(backslash(s), "'", "\\'"))
}

func backslash(s string) string {
	return strings.ReplaceAll(s, "\\", "\\\\")
}

func validateRequiredField(value string, fieldName string, context string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s cannot be empty for %s queries", fieldName, context)
	}
	return nil
}

func appendClusterClause(tokens []string, clusterName *string) []string {
	if clusterName != nil {
		tokens = append(tokens, "ON", "CLUSTER", quote(*clusterName))
	}
	return tokens
}

func isNilOrEmpty(value *string) bool {
	return value == nil || strings.TrimSpace(*value) == ""
}

func qualifiedIdentifier(parts ...string) string {
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		tokens = append(tokens, backtick(strings.TrimSpace(part)))
	}
	return strings.Join(tokens, ".")
}

func rawOrQualifiedIdentifier(value string) string {
	if strings.ContainsAny(value, "`() ") {
		return value
	}

	return qualifiedIdentifier(strings.Split(value, ".")...)
}

// buildDefinitions applies a builder function to each item and collects the results.
func buildDefinitions[T any](items []T, builder func(T) (string, error)) ([]string, error) {
	var errs []error
	results := make([]string, 0, len(items))
	for _, item := range items {
		result, err := builder(item)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		results = append(results, result)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return results, nil
}

// granteeClause renders a TO clause target: ALL EXCEPT a list, ALL, or an explicit grantee list; empty when nothing is set.
func granteeClause(names []string, all bool, allExcept []string) string {
	if len(allExcept) > 0 {
		return "ALL EXCEPT " + strings.Join(backtickAll(allExcept), ", ")
	}
	if all {
		return "ALL"
	}
	return strings.Join(backtickAll(names), ", ")
}

// identifierOrPattern returns a token suitable for use as a database/table identifier
// in GRANT/REVOKE ON clauses.
//
// ClickHouse supports wildcard grants using an asterisk (`*`) as a suffix on database/table
// names (e.g. `db*.*`, `db.table*`). Quoting the pattern (e.g. with backticks) disables
// wildcard matching by turning the pattern into a literal identifier.
func identifierOrPattern(s string) string {
	// Preserve legacy wildcard token.
	if s == "*" {
		return s
	}

	// Only treat a trailing `*` as a wildcard pattern (prefix match), in line with
	// ClickHouse wildcard grant rules.
	if strings.HasSuffix(s, "*") && strings.Count(s, "*") == 1 && !strings.HasPrefix(s, "*") {
		return s
	}
	return backtick(s)
}
