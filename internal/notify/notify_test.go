package notify

import (
	"strings"
	"testing"

	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

func s(v string) *string { return &v }

func TestComposeRespectsRecipientSubscriptions(t *testing.T) {
	team := &storage.Team{Name: "booking-squad"}
	run := &storage.Run{ID: "run-1", Mode: storage.ModeHeal, Status: storage.RunSucceeded,
		Counts: storage.Counts{Total: 3, Valid: 1, Invalid: 1, Generated: 1, Errors: 1}}
	items := []storage.RunItem{
		{TestCaseKey: "ACP-TC-1", Environment: "acp-dev", Outcome: storage.OutcomeValid},
		{TestCaseKey: "ACP-TC-2", Environment: "acp-dev", Outcome: storage.OutcomeRegenerated, OldPNR: s("OLD111"), NewPNR: s("NEW222")},
		{TestCaseKey: "ACP-TC-3", Environment: "acp-dev", Outcome: storage.OutcomeCheckError, Error: s("PSS timeout")},
	}
	recipients := []storage.Recipient{
		{Email: "all@x.com", OnScan: true, OnGeneration: true, OnError: true},
		{Email: "errors@x.com", OnError: true},
		{Email: "nothing@x.com"},
	}

	emails := Compose(team, run, items, recipients, "https://tdms.example")
	if len(emails) != 2 {
		t.Fatalf("got %d emails, want 2 (the unsubscribed recipient gets none)", len(emails))
	}
	all, errs := emails[0], emails[1]
	for _, want := range []string{"SCAN REPORT", "TEST DATA GENERATED", "OLD111 -> NEW222", "ERRORS", "PSS timeout", "https://tdms.example/runs/run-1"} {
		if !strings.Contains(all.Body, want) {
			t.Errorf("full email missing %q", want)
		}
	}
	if strings.Contains(errs.Body, "SCAN REPORT") || strings.Contains(errs.Body, "TEST DATA GENERATED") {
		t.Error("error-only recipient got sections they didn't subscribe to")
	}
	if !strings.Contains(errs.Body, "PSS timeout") {
		t.Error("error-only recipient missing the error")
	}
}

func TestComposeSkipsErrorOnlyRecipientsOnCleanRun(t *testing.T) {
	run := &storage.Run{ID: "r", Status: storage.RunSucceeded}
	items := []storage.RunItem{{TestCaseKey: "ACP-TC-1", Outcome: storage.OutcomeValid}}
	emails := Compose(&storage.Team{Name: "t"}, run, items, []storage.Recipient{{Email: "e@x.com", OnError: true}}, "")
	if len(emails) != 0 {
		t.Fatalf("got %d emails for a clean run to an error-only recipient", len(emails))
	}
}

func TestSubjectCannotInjectHeaders(t *testing.T) {
	run := &storage.Run{ID: "r", Status: storage.RunSucceeded}
	emails := Compose(&storage.Team{Name: "evil\r\nBcc: x@y.com"}, run, nil,
		[]storage.Recipient{{Email: "a@x.com", OnScan: true}}, "")
	if strings.ContainsAny(emails[0].Subject, "\r\n") {
		t.Fatalf("subject contains a line break: %q", emails[0].Subject)
	}
}
