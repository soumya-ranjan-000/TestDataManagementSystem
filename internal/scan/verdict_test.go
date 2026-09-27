package scan

import (
	"errors"
	"testing"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/dictionary"
	"github.com/soumya-ranjan-000/tdms/internal/model"
	"github.com/soumya-ranjan-000/tdms/internal/pss"
	"github.com/soumya-ranjan-000/tdms/internal/qmetry"
	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

var (
	statusCheck = dictionary.CompiledCheck{
		Rule: "booking_status", Field: "booking.status", Operator: "in",
		Value: []any{"CONFIRMED", "TICKETED"}, ConsumedWhen: "CANCELLED",
	}
	windowCheck = dictionary.CompiledCheck{
		Rule: "checkin_window", Field: "segment.travel_date", Operator: "within",
		Value: map[string]int{"opens_hrs": 48, "closes_hrs": 2},
	}
)

func booking(status string, departure time.Time) *pss.Booking {
	return &pss.Booking{PNR: "ABC123", Status: status,
		Segments: []pss.Segment{{DepartureDatetime: departure.Format(time.RFC3339)}}}
}

func TestJudge(t *testing.T) {
	future := time.Now().AddDate(0, 0, 30)
	past := time.Now().Add(-time.Hour)
	checks := []dictionary.CompiledCheck{statusCheck, windowCheck}

	cases := []struct {
		name       string
		booking    *pss.Booking
		lookupErr  error
		wantKind   verdictKind
		wantReason model.RetireReason
		wantRule   string
	}{
		{"healthy ticketed PNR", booking("ticketed", future), nil, verdictValid, "", ""},
		{"cancelled is consumed", booking("cancelled", future), nil, verdictInvalid, model.ReasonConsumed, "booking_status"},
		{"refunded is dead when consumed_when is CANCELLED", booking("refunded", future), nil, verdictInvalid, model.ReasonDead, "booking_status"},
		{"window closed is expired", booking("ticketed", past), nil, verdictInvalid, model.ReasonExpired, "checkin_window"},
		{"purged PNR is dead", nil, pss.ErrNotFound, verdictInvalid, model.ReasonDead, "pnr_not_found"},
		{"PSS outage is unknown, never invalid", nil, errors.New("timeout"), verdictUnknown, "", ""},
		{"missing departure is unknown", &pss.Booking{Status: "ticketed"}, nil, verdictUnknown, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := judge(tc.booking, tc.lookupErr, checks)
			if v.kind != tc.wantKind || v.reason != tc.wantReason || v.failedRule != tc.wantRule {
				t.Fatalf("got %+v, want kind=%v reason=%q rule=%q", v, tc.wantKind, tc.wantReason, tc.wantRule)
			}
		})
	}
}

func TestRefundedIsConsumedWhenDeclared(t *testing.T) {
	ticketedCancel := statusCheck
	ticketedCancel.ConsumedWhen = "REFUNDED"
	v := judge(booking("refunded", time.Now().AddDate(0, 0, 30)), nil, []dictionary.CompiledCheck{ticketedCancel})
	if v.reason != model.ReasonConsumed {
		t.Fatalf("got reason %q, want consumed", v.reason)
	}
}

func TestKeyNumberSortsNumerically(t *testing.T) {
	cases := map[string]int{"ACP-TC-2": 2, "ACP-TC-10": 10, "ACP-TC-4000": 4000, "NOKEY": 0}
	for key, want := range cases {
		if got := keyNumber(key); got != want {
			t.Errorf("%s: got %d, want %d", key, got, want)
		}
	}
}

func TestFlattenFoldersKeepsTreeOrderAndPaths(t *testing.T) {
	roots := []qmetry.FolderNode{{ID: 1, Name: "root", Children: []qmetry.FolderNode{
		{ID: 2, Name: "a", Children: []qmetry.FolderNode{{ID: 4, Name: "a1"}}},
		{ID: 3, Name: "b"},
	}}}
	got := FlattenFolders(roots)
	wantOrder := []int64{1, 2, 4, 3}
	for i, f := range got {
		if f.ID != wantOrder[i] || f.Position != i {
			t.Fatalf("position %d: got folder %d, want %d", i, f.ID, wantOrder[i])
		}
	}
	if got[2].Path != "root/a/a1" || got[2].Depth != 2 || *got[2].ParentID != 2 || got[0].ParentID != nil {
		t.Fatalf("unexpected a1: %+v", got[2])
	}
}

func TestShouldOrphanOnlyOnCompleteFullListing(t *testing.T) {
	present := []string{"ACP-TC-1"}
	cases := []struct {
		name    string
		scope   storage.RunScope
		present []string
		want    bool
	}{
		{"full scope with results", storage.RunScope{}, present, true},
		{"full scope, zero results (likely a folder typo)", storage.RunScope{}, nil, false},
		{"environment scope", storage.RunScope{Environment: "acp-dev"}, present, false},
		{"selected slots", storage.RunScope{SlotIDs: []string{"x"}}, present, false},
	}
	for _, tc := range cases {
		if got := shouldOrphan(tc.scope, tc.present); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTallyCountsRegeneratedAsInvalidAndGenerated(t *testing.T) {
	var c storage.Counts
	for _, o := range []storage.Outcome{storage.OutcomeValid, storage.OutcomeRegenerated, storage.OutcomeCheckError, storage.OutcomeOrphaned} {
		tally(&c, o)
	}
	want := storage.Counts{Total: 4, Valid: 1, Invalid: 1, Generated: 1, Errors: 1}
	if c != want {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}
