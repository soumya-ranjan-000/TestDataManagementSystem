package scan

import (
	"errors"
	"fmt"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/dictionary"
	"github.com/soumya-ranjan-000/tdms/internal/model"
	"github.com/soumya-ranjan-000/tdms/internal/pss"
	"github.com/soumya-ranjan-000/tdms/internal/rules"
)

type verdictKind int

const (
	verdictValid verdictKind = iota
	verdictInvalid
	// verdictUnknown means the PNR couldn't be checked (PSS unreachable,
	// malformed data). It must never retire an instance: only a positive
	// finding that the data went bad may do that.
	verdictUnknown
)

type verdict struct {
	kind       verdictKind
	reason     model.RetireReason // set when invalid
	failedRule string             // set when invalid
	err        error              // set when unknown
	// checks describes each rule evaluated, in order, for the run log.
	checks []string
}

// judge decides a live PNR's fate from the booking lookup result and the
// slot's compiled rules.
func judge(booking *pss.Booking, lookupErr error, checks []dictionary.CompiledCheck) verdict {
	if errors.Is(lookupErr, pss.ErrNotFound) {
		return verdict{kind: verdictInvalid, reason: model.ReasonDead, failedRule: "pnr_not_found"}
	}
	if lookupErr != nil {
		return verdict{kind: verdictUnknown, err: fmt.Errorf("looking up PNR: %w", lookupErr)}
	}
	snap := pss.Snapshot(booking)
	var described []string
	for _, check := range checks {
		ok, err := rules.Evaluate(check, snap)
		if err != nil {
			return verdict{kind: verdictUnknown, err: fmt.Errorf("evaluating %s: %w", check.Rule, err), checks: described}
		}
		described = append(described, describeCheck(check, snap, ok))
		if !ok {
			return verdict{kind: verdictInvalid, reason: retireReason(check, snap), failedRule: check.Rule, checks: described}
		}
	}
	return verdict{kind: verdictValid, checks: described}
}

// describeCheck renders one rule's result with the live value it saw.
func describeCheck(check dictionary.CompiledCheck, snap rules.PNRSnapshot, ok bool) string {
	result := "PASS"
	if !ok {
		result = "FAIL"
	}
	switch check.Rule {
	case "booking_status":
		verb := "is in"
		if !ok {
			verb = "is not in"
		}
		return fmt.Sprintf("Rule booking_status: %s — status %v %s %v", result, snap["booking.status"], verb, check.Value)
	case "checkin_window":
		if dep, isTime := snap["segment.travel_date"].(time.Time); isTime {
			if hrs, isMap := check.Value.(map[string]int); isMap {
				closes := dep.Add(-time.Duration(hrs["closes_hrs"]) * time.Hour)
				return fmt.Sprintf("Rule checkin_window: %s — departs %s, check-in closes %s",
					result, dep.UTC().Format("2006-01-02 15:04 MST"), closes.UTC().Format("2006-01-02 15:04 MST"))
			}
		}
	}
	return fmt.Sprintf("Rule %s: %s — %s %s %v (live value %v)", check.Rule, result, check.Field, check.Operator, check.Value, snap[check.Field])
}

// retireReason maps a failed rule to the lifecycle's failure reasons:
// Expired for a lapsed time rule, Consumed for the rule's declared terminal
// state, Dead for anything else the booking turned into.
func retireReason(check dictionary.CompiledCheck, snap rules.PNRSnapshot) model.RetireReason {
	switch check.Rule {
	case "checkin_window":
		return model.ReasonExpired
	case "booking_status":
		if check.ConsumedWhen != "" && fmt.Sprint(snap["booking.status"]) == check.ConsumedWhen {
			return model.ReasonConsumed
		}
		return model.ReasonDead
	default:
		return model.ReasonDead
	}
}

// compile validates every rule of a block against the dictionary.
func compile(dict *dictionary.Dictionary, block model.Block) ([]dictionary.CompiledCheck, error) {
	var checks []dictionary.CompiledCheck
	for _, rule := range block.Validity.Rules {
		c, err := dict.Compile(rule)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", rule.Name, err)
		}
		checks = append(checks, c...)
	}
	return checks, nil
}
