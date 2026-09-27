// Package rules is the one generic evaluator that checks any compiled rule
// against a live PNR, per "one generic evaluator can check any rule
// against a live PNR." It never templates or validates rules itself —
// that's the dictionary's job — it only answers pass/fail given an already
// legal check.
package rules

import (
	"fmt"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/dictionary"
)

// PNRSnapshot is the live state TDMS reads back from the airline system,
// keyed by the same canonical field paths the dictionary defines.
type PNRSnapshot map[string]any

// Evaluate runs one compiled check against a live PNR snapshot and reports
// whether the instance is still valid by this rule right now.
func Evaluate(check dictionary.CompiledCheck, snap PNRSnapshot) (bool, error) {
	switch check.Operator {
	case "equals":
		return evalEquals(check, snap)
	case "in":
		return evalIn(check, snap)
	case "before":
		return evalTimeCompare(check, snap, time.Time.Before)
	case "after":
		return evalTimeCompare(check, snap, time.Time.After)
	case "within":
		return evalWithin(check, snap)
	default:
		return false, fmt.Errorf("no evaluator registered for operator %q", check.Operator)
	}
}

func fieldValue(check dictionary.CompiledCheck, snap PNRSnapshot) (any, error) {
	v, ok := snap[check.Field]
	if !ok {
		return nil, fmt.Errorf("live PNR snapshot has no value for field %q", check.Field)
	}
	return v, nil
}

func evalEquals(check dictionary.CompiledCheck, snap PNRSnapshot) (bool, error) {
	actual, err := fieldValue(check, snap)
	if err != nil {
		return false, err
	}
	return fmt.Sprint(actual) == fmt.Sprint(check.Value), nil
}

func evalIn(check dictionary.CompiledCheck, snap PNRSnapshot) (bool, error) {
	actual, err := fieldValue(check, snap)
	if err != nil {
		return false, err
	}
	actualStr := fmt.Sprint(actual)
	list, ok := check.Value.([]any)
	if !ok {
		return false, fmt.Errorf("operator \"in\" needs a list value, got %T", check.Value)
	}
	for _, v := range list {
		if fmt.Sprint(v) == actualStr {
			return true, nil
		}
	}
	return false, nil
}

func fieldTime(check dictionary.CompiledCheck, snap PNRSnapshot) (time.Time, error) {
	actual, err := fieldValue(check, snap)
	if err != nil {
		return time.Time{}, err
	}
	t, ok := actual.(time.Time)
	if !ok {
		return time.Time{}, fmt.Errorf("field %q is not a datetime in the snapshot (got %T)", check.Field, actual)
	}
	return t, nil
}

func evalTimeCompare(check dictionary.CompiledCheck, snap PNRSnapshot, cmp func(time.Time, time.Time) bool) (bool, error) {
	t, err := fieldTime(check, snap)
	if err != nil {
		return false, err
	}
	bound, ok := check.Value.(time.Time)
	if !ok {
		return false, fmt.Errorf("operator needs a time.Time bound, got %T", check.Value)
	}
	return cmp(t, bound), nil
}

// evalWithin implements the check-in-window rule: now must fall between
// (travel_date - opens_hrs) and (travel_date - closes_hrs) — "now is
// before travel_date minus 48h", generalized to any bound pair.
func evalWithin(check dictionary.CompiledCheck, snap PNRSnapshot) (bool, error) {
	travelDate, err := fieldTime(check, snap)
	if err != nil {
		return false, err
	}
	params, ok := check.Value.(map[string]int)
	if !ok {
		return false, fmt.Errorf("checkin_window check needs opens_hrs/closes_hrs, got %T", check.Value)
	}
	opens := travelDate.Add(-time.Duration(params["opens_hrs"]) * time.Hour)
	closes := travelDate.Add(-time.Duration(params["closes_hrs"]) * time.Hour)
	now := time.Now().UTC()
	return !now.Before(opens) && !now.After(closes), nil
}
