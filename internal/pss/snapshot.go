package pss

import (
	"strings"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/rules"
)

// Snapshot converts a live PSS booking into the canonical field values the
// dictionary and evaluator expect. PSS's own status strings are lowercase
// and hyphenated (confirmed by hitting the live deployment — e.g.
// "checked-in"), while the dictionary's canonical values are
// upper-snake-case ("CHECKED_IN") to match the design doc's own
// CONFIRMED/TICKETED/CANCELLED example. This is exactly the normalization
// layer the design doc anticipates for real-world value variance — done
// here as a direct mapping, since only one source system exists so far;
// see the dictionary's own docs for when this graduates to a general
// synonym/aliasing layer.
func Snapshot(b *Booking) rules.PNRSnapshot {
	snap := rules.PNRSnapshot{
		"booking.status": canonicalStatus(b.Status),
	}
	if len(b.Segments) > 0 {
		if t, err := time.Parse(time.RFC3339, b.Segments[0].DepartureDatetime); err == nil {
			snap["segment.travel_date"] = t
		}
	}
	return snap
}

func canonicalStatus(raw string) string {
	return strings.ToUpper(strings.ReplaceAll(raw, "-", "_"))
}
