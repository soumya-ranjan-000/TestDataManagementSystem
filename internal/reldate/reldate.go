// Package reldate resolves the authoring block's relative date expressions
// (T+90d, T+1w, T+1) against a given moment. "Relative dates make the block
// a template that regenerates correctly forever" — this is that resolution
// step, run fresh every time an instance is generated.
package reldate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var pattern = regexp.MustCompile(`^T\+(\d+)([dw]?)$`)

// Parse resolves expr (e.g. "T+90d", "T+1w", "T+1" for tomorrow) to a
// calendar date offset from now, with no time-of-day component — PSS
// bookings are date-only.
func Parse(expr string, now time.Time) (time.Time, error) {
	m := pattern.FindStringSubmatch(strings.TrimSpace(expr))
	if m == nil {
		return time.Time{}, fmt.Errorf("relative date %q doesn't match the T+<n>[d|w] grammar", expr)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("relative date %q: %w", expr, err)
	}
	days := n
	if m[2] == "w" {
		days *= 7
	}
	base := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return base.AddDate(0, 0, days), nil
}
