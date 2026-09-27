package scheduler

import (
	"testing"
	"time"
)

func TestParseRunTimes(t *testing.T) {
	got, err := ParseRunTimes("14:00, 02:30 14:00")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "02:30" || got[1] != "14:00" {
		t.Fatalf("got %v, want sorted de-duplicated [02:30 14:00]", got)
	}
	for _, bad := range []string{"24:00", "2:30", "14:60", "noon"} {
		if _, err := ParseRunTimes(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestLatestDueRespectsTimezoneAndGrace(t *testing.T) {
	dubai, _ := time.LoadLocation("Asia/Dubai") // UTC+4
	runTimes := []string{"02:00", "14:00"}
	// 10:05 UTC is 14:05 in Dubai: the 14:00 run is 5 minutes late.
	now := time.Date(2026, 9, 27, 10, 5, 0, 0, time.UTC)

	due, ok := LatestDue(runTimes, dubai, now, Grace)
	if !ok {
		t.Fatal("expected the 14:00 Dubai run to be due")
	}
	if want := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC); !due.Equal(want) {
		t.Fatalf("due = %s, want %s", due.UTC(), want)
	}

	// 20 minutes late is past the grace window.
	if _, ok := LatestDue(runTimes, dubai, now.Add(15*time.Minute), Grace); ok {
		t.Fatal("expected nothing due outside the grace window")
	}
}

func TestLatestDueAcrossMidnight(t *testing.T) {
	// A 23:55 run caught by a tick at 00:05 the next day.
	now := time.Date(2026, 9, 28, 0, 5, 0, 0, time.UTC)
	due, ok := LatestDue([]string{"23:55"}, time.UTC, now, Grace)
	if !ok || !due.Equal(time.Date(2026, 9, 27, 23, 55, 0, 0, time.UTC)) {
		t.Fatalf("got %s, %v", due, ok)
	}
}

func TestNext(t *testing.T) {
	now := time.Date(2026, 9, 27, 15, 0, 0, 0, time.UTC)
	next, ok := Next([]string{"02:00", "14:00"}, time.UTC, now)
	if !ok || !next.Equal(time.Date(2026, 9, 28, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %s, %v", next, ok)
	}
	if _, ok := Next(nil, time.UTC, now); ok {
		t.Fatal("expected no next run without run times")
	}
}
