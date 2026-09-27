package reldate

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	now := time.Date(2026, 9, 27, 18, 30, 0, 0, time.UTC)
	cases := map[string]string{
		"T+90d": "2026-12-26",
		"T+1w":  "2026-10-04",
		"T+1":   "2026-09-28",
		" T+0d": "2026-09-27",
	}
	for expr, want := range cases {
		got, err := Parse(expr, now)
		if err != nil {
			t.Fatalf("%q: %v", expr, err)
		}
		if got.Format("2006-01-02") != want {
			t.Errorf("%q: got %s, want %s", expr, got.Format("2006-01-02"), want)
		}
	}
}

func TestParseRejectsBadGrammar(t *testing.T) {
	for _, expr := range []string{"", "90d", "T-1d", "T+1m", "2026-12-26"} {
		if _, err := Parse(expr, time.Now()); err == nil {
			t.Errorf("%q: expected an error", expr)
		}
	}
}
