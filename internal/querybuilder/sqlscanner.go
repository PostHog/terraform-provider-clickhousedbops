package querybuilder

import (
	"fmt"
	"strings"
)

// SQLScanState tracks delimiter nesting while scanning raw SQL character by character.
// It handles parentheses, brackets, braces, and quoted strings (single, double, backtick).
type SQLScanState struct {
	ParenDepth   int
	BracketDepth int
	BraceDepth   int
	InQuote      byte
}

// IsTopLevel returns true when the scanner is outside all delimiters and quotes.
func (s SQLScanState) IsTopLevel() bool {
	return s.InQuote == 0 && s.ParenDepth == 0 && s.BracketDepth == 0 && s.BraceDepth == 0
}

// AdvanceSQLScanState processes the character at raw[index] and returns the
// (possibly advanced) index and any scanning error.
func AdvanceSQLScanState(raw string, index int, state *SQLScanState) (int, error) {
	ch := raw[index]
	if state.InQuote != 0 {
		switch state.InQuote {
		case '\'':
			if ch == '\\' {
				if index+1 < len(raw) {
					return index + 1, nil
				}
				return index, fmt.Errorf("unterminated escape")
			}
			if ch == '\'' {
				if index+1 < len(raw) && raw[index+1] == '\'' {
					return index + 1, nil
				}
				state.InQuote = 0
			}
		case '"':
			if ch == '\\' {
				if index+1 < len(raw) {
					return index + 1, nil
				}
				return index, fmt.Errorf("unterminated escape")
			}
			if ch == '"' {
				state.InQuote = 0
			}
		case '`':
			if ch == '\\' {
				if index+1 < len(raw) {
					return index + 1, nil
				}
				return index, fmt.Errorf("unterminated escape")
			}
			if ch == '`' {
				if index+1 < len(raw) && raw[index+1] == '`' {
					return index + 1, nil
				}
				state.InQuote = 0
			}
		}
		return index, nil
	}

	switch ch {
	case '\'', '"', '`':
		state.InQuote = ch
	case '(':
		state.ParenDepth++
	case ')':
		state.ParenDepth--
	case '[':
		state.BracketDepth++
	case ']':
		state.BracketDepth--
	case '{':
		state.BraceDepth++
	case '}':
		state.BraceDepth--
	}

	if state.ParenDepth < 0 || state.BracketDepth < 0 || state.BraceDepth < 0 {
		return index, fmt.Errorf("unbalanced delimiters")
	}

	return index, nil
}

// NormalizeSQL returns a whitespace-insensitive form of a SQL fragment for comparison.
// Outside quotes it collapses whitespace runs to one space, removes whitespace after
// ( and [ and before ) ] and , and puts exactly one space after every comma.
// Quoted text is never changed.
func NormalizeSQL(raw string) string {
	var out strings.Builder
	state := SQLScanState{}
	pendingSpace := false
	last := byte(0)
	for index := 0; index < len(raw); index++ {
		ch := raw[index]
		inQuote := state.InQuote != 0
		if !inQuote {
			switch ch {
			case ' ', '\t', '\n', '\r':
				pendingSpace = true
				continue
			case ')', ']', ',':
				pendingSpace = false
			}
			if pendingSpace && last != 0 && last != '(' && last != '[' {
				out.WriteByte(' ')
			}
			pendingSpace = false
		}

		next, err := AdvanceSQLScanState(raw, index, &state)
		if err != nil {
			return strings.TrimSpace(raw)
		}
		out.WriteString(raw[index : next+1])
		index = next
		last = ch
		if !inQuote && ch == ',' {
			pendingSpace = true
		}
	}

	return out.String()
}
