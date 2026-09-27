package dictionary

import (
	"fmt"

	"github.com/soumya-ranjan-000/tdms/internal/model"
)

// CompiledCheck is one generic (field, operator, value) test — the "fixed
// anatomy" every validity rule reduces to, however it's templated in YAML.
//
// Rule and ConsumedWhen carry enough of the source template for a failed
// check to be classified into a retire reason (consumed/expired/dead).
type CompiledCheck struct {
	Rule         string
	Field        string
	Operator     string
	Value        any
	ConsumedWhen string
}

// Compile expands a named rule template into one or more generic checks,
// validating each against the dictionary as it goes. The concrete entry
// formats are a build-time detail (per the design doc); these two
// templates — booking_status and checkin_window — are that detail, chosen
// to match the doc's own worked example exactly. Adding a new rule means
// adding a new case here plus whatever fields/operators it needs in Seed.
func (d *Dictionary) Compile(rule model.Rule) ([]CompiledCheck, error) {
	switch rule.Name {
	case "booking_status":
		return d.compileBookingStatus(rule)
	case "checkin_window":
		return d.compileCheckinWindow(rule)
	default:
		return nil, fmt.Errorf("unknown rule template %q (dictionary v%s doesn't define it)", rule.Name, d.Version)
	}
}

func (d *Dictionary) compileBookingStatus(rule model.Rule) ([]CompiledCheck, error) {
	validWhen, ok := rule.Params["valid_when"].([]any)
	if !ok || len(validWhen) == 0 {
		return nil, fmt.Errorf("booking_status: valid_when is required and must be a non-empty list")
	}
	check := CompiledCheck{Rule: "booking_status", Field: "booking.status", Operator: "in", Value: validWhen}
	if err := d.validateCheck(check); err != nil {
		return nil, fmt.Errorf("booking_status: %w", err)
	}
	if raw, present := rule.Params["consumed_when"]; present {
		consumed, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("booking_status: consumed_when must be a single status")
		}
		if err := d.validateCheck(CompiledCheck{Field: "booking.status", Operator: "equals", Value: consumed}); err != nil {
			return nil, fmt.Errorf("booking_status: consumed_when: %w", err)
		}
		check.ConsumedWhen = consumed
	}
	return []CompiledCheck{check}, nil
}

func (d *Dictionary) compileCheckinWindow(rule model.Rule) ([]CompiledCheck, error) {
	opens, opensOK := asInt(rule.Params["opens_hrs"])
	closes, closesOK := asInt(rule.Params["closes_hrs"])
	if !opensOK || !closesOK {
		return nil, fmt.Errorf("checkin_window: opens_hrs and closes_hrs are required integers")
	}
	check := CompiledCheck{
		Rule:     "checkin_window",
		Field:    "segment.travel_date",
		Operator: "within",
		Value:    map[string]int{"opens_hrs": opens, "closes_hrs": closes},
	}
	if err := d.validateCheck(check); err != nil {
		return nil, fmt.Errorf("checkin_window: %w", err)
	}
	return []CompiledCheck{check}, nil
}

// validateCheck is the one check every rule must pass: the field must be
// in the dictionary, the operator must apply to that field's type, and any
// enum value(s) must be in that field's allowed set.
func (d *Dictionary) validateCheck(c CompiledCheck) error {
	field, ok := d.Field(c.Field)
	if !ok {
		return fmt.Errorf("field %q is not in the dictionary", c.Field)
	}
	if !d.OperatorAppliesTo(c.Operator, field.Type) {
		return fmt.Errorf("operator %q does not apply to field %q (type %s)", c.Operator, c.Field, field.Type)
	}
	if field.Type != TypeEnum {
		return nil
	}
	switch v := c.Value.(type) {
	case []any:
		for _, item := range v {
			s := fmt.Sprint(item)
			if !contains(field.AllowedValues, s) {
				return fmt.Errorf("value %q is not a legal value for field %q", s, c.Field)
			}
		}
	case string:
		if !contains(field.AllowedValues, v) {
			return fmt.Errorf("value %q is not a legal value for field %q", v, c.Field)
		}
	}
	return nil
}

// asInt accepts a whole number in any form a rule param arrives in: int
// from YAML, float64 once a stored block has round-tripped through JSONB.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
