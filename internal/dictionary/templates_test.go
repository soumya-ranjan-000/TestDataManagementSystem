package dictionary

import (
	"testing"

	"github.com/soumya-ranjan-000/tdms/internal/model"
)

func TestCompileBookingStatusCarriesRuleAndConsumedWhen(t *testing.T) {
	rule := model.Rule{Name: "booking_status", Params: map[string]any{
		"valid_when":    []any{"CONFIRMED", "TICKETED"},
		"consumed_when": "CANCELLED",
	}}
	checks, err := Seed().Compile(rule)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 {
		t.Fatalf("got %d checks, want 1", len(checks))
	}
	if checks[0].Rule != "booking_status" || checks[0].ConsumedWhen != "CANCELLED" {
		t.Fatalf("got %+v", checks[0])
	}
}

func TestCompileRejectsIllegalConsumedWhen(t *testing.T) {
	rule := model.Rule{Name: "booking_status", Params: map[string]any{
		"valid_when":    []any{"CONFIRMED"},
		"consumed_when": "EXPLODED",
	}}
	if _, err := Seed().Compile(rule); err == nil {
		t.Fatal("expected consumed_when outside the enum to be rejected")
	}
}

func TestCompileCheckinWindowCarriesRule(t *testing.T) {
	rule := model.Rule{Name: "checkin_window", Params: map[string]any{"opens_hrs": 48, "closes_hrs": 2}}
	checks, err := Seed().Compile(rule)
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].Rule != "checkin_window" {
		t.Fatalf("got rule %q", checks[0].Rule)
	}
}

func TestCompileCheckinWindowAcceptsJSONRoundTrippedNumbers(t *testing.T) {
	rule := model.Rule{Name: "checkin_window", Params: map[string]any{"opens_hrs": float64(48), "closes_hrs": float64(2)}}
	checks, err := Seed().Compile(rule)
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].Value.(map[string]int)["closes_hrs"] != 2 {
		t.Fatalf("got %+v", checks[0].Value)
	}
	fractional := model.Rule{Name: "checkin_window", Params: map[string]any{"opens_hrs": 48.5, "closes_hrs": float64(2)}}
	if _, err := Seed().Compile(fractional); err == nil {
		t.Fatal("expected a fractional hour count to be rejected")
	}
}

func TestCompileRejectsUnknownTemplate(t *testing.T) {
	if _, err := Seed().Compile(model.Rule{Name: "nope"}); err == nil {
		t.Fatal("expected an unknown template to be rejected")
	}
}
