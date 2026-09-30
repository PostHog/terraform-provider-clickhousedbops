package dbops

import "testing"

func TestKeyExpression(t *testing.T) {
	for key, want := range map[string]string{
		"k":                                      "k",
		"domain, kind":                           "(domain, kind)",
		"toYYYYMM(d)":                            "toYYYYMM(d)",
		"toStartOfInterval(d, toIntervalDay(1))": "toStartOfInterval(d, toIntervalDay(1))",
		"team_id, toDate(ts)":                    "(team_id, toDate(ts))",
		"concat('a,b', k)":                       "concat('a,b', k)",
	} {
		if got := keyExpression(key); got != want {
			t.Errorf("keyExpression(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestDataParamEscapesForStringParameters(t *testing.T) {
	got := dataParam("a\tb\n\\c\r")["data"]
	if want := `a\tb\n\\c\r`; got != want {
		t.Errorf("dataParam() = %q, want %q", got, want)
	}
}
