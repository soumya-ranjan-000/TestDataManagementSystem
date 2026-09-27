package rules

import (
	"testing"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/dictionary"
)

func checkinCheck() dictionary.CompiledCheck {
	return dictionary.CompiledCheck{
		Rule:     "checkin_window",
		Field:    "segment.travel_date",
		Operator: "within",
		Value:    map[string]int{"opens_hrs": 48, "closes_hrs": 2},
	}
}

func TestCheckinWindowStaysValidUntilWindowCloses(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		departure time.Time
		want      bool
	}{
		{"freshly generated, 90 days out", now.AddDate(0, 0, 90), true},
		{"window open", now.Add(24 * time.Hour), true},
		{"exactly at close", now.Add(2 * time.Hour), true},
		{"window closed", now.Add(1 * time.Hour), false},
		{"already departed", now.Add(-5 * time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := PNRSnapshot{"segment.travel_date": tc.departure}
			got, err := evalWithinAt(checkinCheck(), snap, now)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got valid=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestBookingStatusIn(t *testing.T) {
	check := dictionary.CompiledCheck{
		Rule:     "booking_status",
		Field:    "booking.status",
		Operator: "in",
		Value:    []any{"CONFIRMED", "TICKETED"},
	}
	for status, want := range map[string]bool{"TICKETED": true, "CONFIRMED": true, "CANCELLED": false} {
		got, err := Evaluate(check, PNRSnapshot{"booking.status": status})
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("status %s: got %v, want %v", status, got, want)
		}
	}
}

func TestMissingFieldIsAnErrorNotAFailure(t *testing.T) {
	_, err := Evaluate(checkinCheck(), PNRSnapshot{})
	if err == nil {
		t.Fatal("expected an error for a snapshot missing the field")
	}
}
