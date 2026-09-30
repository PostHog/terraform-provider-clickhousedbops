package querybuilder

import "testing"

// helper that scans an entire string and returns the final state and any error.
func scanFull(input string) (SQLScanState, error) {
	var state SQLScanState
	for i := 0; i < len(input); {
		next, err := AdvanceSQLScanState(input, i, &state)
		if err != nil {
			return state, err
		}
		i = next + 1
	}
	return state, nil
}

func TestAdvanceSQLScanState(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantErr     bool
		errContains string
		// expected final state fields
		wantInQuote    byte
		wantParenD     int
		wantBracketD   int
		wantBraceD     int
		wantIsTopLevel bool
	}{
		// Basic quotes
		{
			name:           "single quoted string opened and closed",
			input:          "'hello'",
			wantIsTopLevel: true,
		},
		{
			name:           "double quoted string opened and closed",
			input:          `"hello"`,
			wantIsTopLevel: true,
		},
		{
			name:           "backtick quoted string opened and closed",
			input:          "`hello`",
			wantIsTopLevel: true,
		},
		{
			name:           "unterminated single quote",
			input:          "'hello",
			wantInQuote:    '\'',
			wantIsTopLevel: false,
		},
		{
			name:           "unterminated double quote",
			input:          `"hello`,
			wantInQuote:    '"',
			wantIsTopLevel: false,
		},
		{
			name:           "unterminated backtick",
			input:          "`hello",
			wantInQuote:    '`',
			wantIsTopLevel: false,
		},

		// Escape sequences inside quotes
		{
			name:           "backslash escape in single quotes",
			input:          `'he\'llo'`,
			wantIsTopLevel: true,
		},
		{
			name:           "backslash escape in double quotes",
			input:          `"he\"llo"`,
			wantIsTopLevel: true,
		},
		{
			name:           "backslash escape in backticks",
			input:          "`he\\`llo`",
			wantIsTopLevel: true,
		},
		{
			name:        "unterminated escape at end of single quoted string",
			input:       `'\`,
			wantErr:     true,
			errContains: "unterminated escape",
		},
		{
			name:        "unterminated escape at end of double quoted string",
			input:       `"\`,
			wantErr:     true,
			errContains: "unterminated escape",
		},
		{
			name:        "unterminated escape at end of backtick string",
			input:       "`\\",
			wantErr:     true,
			errContains: "unterminated escape",
		},

		// Doubled-quote escaping
		{
			name:           "doubled single quote escape",
			input:          "'it''s'",
			wantIsTopLevel: true,
		},
		{
			name:           "doubled backtick escape",
			input:          "```col``name```",
			wantIsTopLevel: true,
		},
		{
			name:           "doubled single quote at end closes correctly",
			input:          "'a'''",
			wantIsTopLevel: true,
		},

		// Parentheses tracking
		{
			name:           "simple parentheses",
			input:          "(a)",
			wantIsTopLevel: true,
		},
		{
			name:           "nested parentheses",
			input:          "((a)(b))",
			wantIsTopLevel: true,
		},
		{
			name:           "unclosed parenthesis",
			input:          "(a",
			wantParenD:     1,
			wantIsTopLevel: false,
		},
		{
			name:        "unbalanced close paren",
			input:       ")",
			wantErr:     true,
			errContains: "unbalanced delimiters",
		},

		// Bracket tracking
		{
			name:           "simple brackets",
			input:          "[x]",
			wantIsTopLevel: true,
		},
		{
			name:        "unbalanced close bracket",
			input:       "]",
			wantErr:     true,
			errContains: "unbalanced delimiters",
		},

		// Brace tracking
		{
			name:           "simple braces",
			input:          "{x}",
			wantIsTopLevel: true,
		},
		{
			name:        "unbalanced close brace",
			input:       "}",
			wantErr:     true,
			errContains: "unbalanced delimiters",
		},

		// Mixed delimiters
		{
			name:           "nested mixed delimiters",
			input:          "({[a]})",
			wantIsTopLevel: true,
		},

		// Parens inside quotes should not be tracked
		{
			name:           "parens inside single quotes are ignored",
			input:          "'(('",
			wantIsTopLevel: true,
		},
		{
			name:           "parens inside double quotes are ignored",
			input:          `"()"`,
			wantIsTopLevel: true,
		},
		{
			name:           "parens inside backticks are ignored",
			input:          "`)`",
			wantIsTopLevel: true,
		},
		{
			name:           "brackets and braces inside quotes are ignored",
			input:          "'[{]}'",
			wantIsTopLevel: true,
		},

		// Mixed scenarios
		{
			name:           "function call with quoted arg containing parens",
			input:          "foo('bar()')",
			wantIsTopLevel: true,
		},
		{
			name:           "complex expression with quotes and parens",
			input:          `SELECT * FROM t WHERE x IN ('a','b')`,
			wantIsTopLevel: true,
		},
		{
			name:           "empty string",
			input:          "",
			wantIsTopLevel: true,
		},
		{
			name:           "plain text no delimiters",
			input:          "hello world",
			wantIsTopLevel: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, err := scanFull(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errContains)
				}
				if tt.errContains != "" {
					if got := err.Error(); !containsSubstring(got, tt.errContains) {
						t.Fatalf("error = %q, want it to contain %q", got, tt.errContains)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if state.InQuote != tt.wantInQuote {
				t.Errorf("InQuote = %q, want %q", state.InQuote, tt.wantInQuote)
			}
			if state.ParenDepth != tt.wantParenD {
				t.Errorf("ParenDepth = %d, want %d", state.ParenDepth, tt.wantParenD)
			}
			if state.BracketDepth != tt.wantBracketD {
				t.Errorf("BracketDepth = %d, want %d", state.BracketDepth, tt.wantBracketD)
			}
			if state.BraceDepth != tt.wantBraceD {
				t.Errorf("BraceDepth = %d, want %d", state.BraceDepth, tt.wantBraceD)
			}
			if got := state.IsTopLevel(); got != tt.wantIsTopLevel {
				t.Errorf("IsTopLevel() = %v, want %v", got, tt.wantIsTopLevel)
			}
		})
	}
}

func TestIsTopLevel(t *testing.T) {
	tests := []struct {
		name  string
		state SQLScanState
		want  bool
	}{
		{
			name:  "zero value is top level",
			state: SQLScanState{},
			want:  true,
		},
		{
			name:  "in single quote is not top level",
			state: SQLScanState{InQuote: '\''},
			want:  false,
		},
		{
			name:  "in double quote is not top level",
			state: SQLScanState{InQuote: '"'},
			want:  false,
		},
		{
			name:  "in backtick is not top level",
			state: SQLScanState{InQuote: '`'},
			want:  false,
		},
		{
			name:  "paren depth non-zero is not top level",
			state: SQLScanState{ParenDepth: 1},
			want:  false,
		},
		{
			name:  "bracket depth non-zero is not top level",
			state: SQLScanState{BracketDepth: 2},
			want:  false,
		},
		{
			name:  "brace depth non-zero is not top level",
			state: SQLScanState{BraceDepth: 1},
			want:  false,
		},
		{
			name:  "multiple non-zero fields is not top level",
			state: SQLScanState{ParenDepth: 1, InQuote: '\''},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.state.IsTopLevel(); got != tt.want {
				t.Errorf("IsTopLevel() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsTopLevelTransitions verifies that IsTopLevel flips correctly as we
// scan through a string that enters and exits delimiters.
func TestIsTopLevelTransitions(t *testing.T) {
	// Input: a'b'(c)
	// Index: 0123 456
	// Expected IsTopLevel after processing each char:
	//   0:'a' -> true   (plain char)
	//   1:'\'' -> false  (enter quote)
	//   2:'b' -> false   (inside quote)
	//   3:'\'' -> true   (exit quote)
	//   4:'(' -> false   (enter paren)
	//   5:'c' -> false   (inside paren)
	//   6:')' -> true    (exit paren)
	input := "a'b'(c)"
	wantTopLevel := []bool{true, false, false, true, false, false, true}

	var state SQLScanState
	for i := 0; i < len(input); i++ {
		next, err := AdvanceSQLScanState(input, i, &state)
		if err != nil {
			t.Fatalf("unexpected error at index %d: %v", i, err)
		}
		if next != i {
			t.Fatalf("unexpected index advance at %d: got %d", i, next)
		}
		if got := state.IsTopLevel(); got != wantTopLevel[i] {
			t.Errorf("index %d (%q): IsTopLevel() = %v, want %v", i, input[i], got, wantTopLevel[i])
		}
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || indexSubstring(s, substr) >= 0)
}

func indexSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func TestNormalizeSQL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "trims and collapses whitespace", raw: "  SELECT\n    a\tFROM   t ", want: "SELECT a FROM t"},
		{name: "pretty-printed list equals single line", raw: "(\n  a,\n  b\n)", want: "(a, b)"},
		{name: "adds the space after a comma", raw: "f(a,b,[1,2])", want: "f(a, b, [1, 2])"},
		{name: "removes whitespace inside brackets", raw: "f( a , [ 1 ] )", want: "f(a, [1])"},
		{name: "keeps single-quoted text", raw: "x = 'a ,  b( c )\n'", want: "x = 'a ,  b( c )\n'"},
		{name: "keeps backticked identifiers", raw: "`a  ,b` ,  c", want: "`a  ,b`, c"},
		{name: "keeps escaped quotes inside strings", raw: `'it\'s ,  x'  ,y`, want: `'it\'s ,  x', y`},
		{name: "multi-line query", raw: "SELECT\n    id,\n    count() AS c\nFROM db.t\nWHERE id IN (\n    1,\n    2\n)", want: "SELECT id, count() AS c FROM db.t WHERE id IN (1, 2)"},
		{name: "unbalanced input is only trimmed", raw: " a ) ", want: "a )"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeSQL(tt.raw); got != tt.want {
				t.Errorf("NormalizeSQL() = %q, want %q", got, tt.want)
			}
		})
	}
}
