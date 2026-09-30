package querybuilder

import (
	"fmt"
	"strings"

	"github.com/pingcap/errors"
)

// FindTopLevelKeyword searches for a keyword at the top level of a SQL fragment
// (outside parentheses and quotes), starting from the given position.
// Returns -1 if the keyword is not found.
func FindTopLevelKeyword(raw string, keyword string, start int) (int, error) {
	state := SQLScanState{}
	for index := 0; index < len(raw); index++ {
		var err error
		index, err = AdvanceSQLScanState(raw, index, &state)
		if err != nil {
			return -1, errors.WithMessage(err, fmt.Sprintf("error scanning SQL near position %d", index))
		}
		if index < start || !state.IsTopLevel() {
			continue
		}
		if !strings.HasPrefix(raw[index:], keyword) {
			continue
		}
		if !IsClauseBoundary(raw, index-1) || !IsClauseBoundary(raw, index+len(keyword)) {
			continue
		}
		return index, nil
	}

	return -1, nil
}

// IsClauseBoundary returns true if the character at index is a whitespace or
// opening paren, or if index is out of bounds (start/end of string).
func IsClauseBoundary(raw string, index int) bool {
	if index < 0 || index >= len(raw) {
		return true
	}
	switch raw[index] {
	case ' ', '\t', '\n', '\r', '(':
		return true
	default:
		return false
	}
}

// SplitTopLevel splits a SQL fragment on the given separator byte at the top
// level (outside parentheses and quotes). Parts are returned untrimmed.
func SplitTopLevel(raw string, separator byte) ([]string, error) {
	parts := make([]string, 0)
	start := 0
	state := SQLScanState{}
	for index := 0; index < len(raw); index++ {
		ch := raw[index]
		var err error
		index, err = AdvanceSQLScanState(raw, index, &state)
		if err != nil {
			return nil, err
		}
		if state.IsTopLevel() && ch == separator {
			parts = append(parts, raw[start:index])
			start = index + 1
		}
	}

	if !state.IsTopLevel() {
		return nil, fmt.Errorf("unbalanced delimiters in SQL fragment")
	}

	parts = append(parts, raw[start:])
	return parts, nil
}

// SplitTopLevelCSV splits a SQL fragment on commas that appear at the top level
// (outside parentheses and quotes). Parts are trimmed and empty parts are dropped.
func SplitTopLevelCSV(raw string) ([]string, error) {
	parts, err := SplitTopLevel(raw, ',')
	if err != nil {
		return nil, err
	}

	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}

	return result, nil
}

// FindTrailingTopLevelParentheses finds the last top-level parenthesized group
// in a SQL fragment. Returns the opening index, closing index, whether found, and error.
func FindTrailingTopLevelParentheses(raw string) (int, int, bool, error) {
	state := SQLScanState{}
	closeIndex := -1
	for index := 0; index < len(raw); index++ {
		var err error
		index, err = AdvanceSQLScanState(raw, index, &state)
		if err != nil {
			return 0, 0, false, err
		}
		if !state.IsTopLevel() {
			continue
		}
		if raw[index] == ')' {
			closeIndex = index
		}
	}
	if closeIndex == -1 {
		return 0, 0, false, nil
	}

	state = SQLScanState{}
	openIndex := -1
	for index := 0; index <= closeIndex; index++ {
		ch := raw[index]
		if state.IsTopLevel() && ch == '(' {
			openIndex = index
		}

		var err error
		index, err = AdvanceSQLScanState(raw, index, &state)
		if err != nil {
			return 0, 0, false, err
		}
	}
	if openIndex == -1 || !state.IsTopLevel() {
		return 0, 0, false, errors.New("unable to parse CREATE VIEW column signature")
	}

	if strings.TrimSpace(raw[closeIndex+1:]) != "" {
		return 0, 0, false, nil
	}

	return openIndex, closeIndex, true, nil
}

// FindMatchingClose finds the closing parenthesis matching the opening paren at
// openIdx in raw. Returns the index of the closing paren.
func FindMatchingClose(raw string, openIdx int) (int, error) {
	depth := 0
	state := SQLScanState{}
	for i := openIdx; i < len(raw); i++ {
		ch := raw[i]
		var err error
		i, err = AdvanceSQLScanState(raw, i, &state)
		if err != nil {
			return 0, err
		}
		if state.InQuote != 0 {
			continue
		}
		if ch == '(' {
			depth++
		} else if ch == ')' {
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, errors.New("unbalanced parentheses")
}

// UnwrapNullableType strips the Nullable(...) wrapper from a type string if present.
// Returns the inner type and true if unwrapped, or the original and false if not wrapped.
func UnwrapNullableType(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "Nullable(") || !strings.HasSuffix(raw, ")") {
		return raw, false
	}

	inner := raw[len("Nullable(") : len(raw)-1]
	state := SQLScanState{}
	for index := 0; index < len(inner); index++ {
		var err error
		index, err = AdvanceSQLScanState(inner, index, &state)
		if err != nil {
			return raw, false
		}
	}
	if !state.IsTopLevel() {
		return raw, false
	}

	return strings.TrimSpace(inner), true
}

// UnquoteIdentifier strips backtick quoting from an identifier.
// Handles both backslash escaping (\`) used by ClickHouse and doubled-backtick
// escaping (“) which is also accepted by the SQL scanner.
func UnquoteIdentifier(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && raw[0] == '`' && raw[len(raw)-1] == '`' {
		inner := raw[1 : len(raw)-1]
		inner = strings.ReplaceAll(inner, "\\`", "`")
		inner = strings.ReplaceAll(inner, "``", "`")
		return inner
	}
	return raw
}

// FindColumnNameEnd finds the position where the column name ends in a
// "name type" pair, scanning past backtick-quoted identifiers.
func FindColumnNameEnd(raw string) (int, error) {
	state := SQLScanState{}
	for index := 0; index < len(raw); index++ {
		var err error
		index, err = AdvanceSQLScanState(raw, index, &state)
		if err != nil {
			return 0, err
		}
		if !state.IsTopLevel() {
			continue
		}
		if raw[index] == ' ' || raw[index] == '\t' || raw[index] == '\n' || raw[index] == '\r' {
			return index, nil
		}
	}
	return 0, errors.New("unable to parse column name")
}
