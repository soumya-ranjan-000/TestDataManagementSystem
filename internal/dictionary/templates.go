package dictionary

import (
	"fmt"

	"github.com/soumya-ranjan-000/tdms/internal/model"
)

// CompiledCheck is one generic (field, operator, value) test — the "fixed
// anatomy" every validity rule reduces to, however it's templated in YAML.
type CompiledCheck struct {
	Field    string
	Operator string
	Value    any
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
	check := CompiledCheck{Field: "booking.status", Operator: "in", Value: validWhen}
	if err := d.validateCheck(check); err != nil {
		return nil, fmt.Errorf("booking_status: %w", err)
	}
	return []CompiledCheck{check}, nil
}

func (d *Dictionary) compileCheckinWindow(rule model.Rule) ([]CompiledCheck, error) {
	opens, opensOK := rule.Params["opens_hrs"].(int)
	closes, closesOK := rule.Params["closes_hrs"].(int)
	if !opensOK || !closesOK {
		return nil, fmt.Errorf("checkin_window: opens_hrs and closes_hrs are required integers")
	}
	check := CompiledCheck{
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

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
