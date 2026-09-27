// Package scheduler queues each team's scheduled heal runs at the daily
// times its admin configured, in the team's own timezone.
package scheduler

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

// Grace is how late a run time may still be caught. A tick that lands
// after a restart or a slow minute still queues the run it missed; the
// (team, scheduled_for) key keeps that from ever queueing twice.
const Grace = 15 * time.Minute

var runTimePattern = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// ParseRunTimes validates a comma- or space-separated list of HH:MM times
// and returns them sorted and de-duplicated.
func ParseRunTimes(input string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if !runTimePattern.MatchString(f) {
			return nil, fmt.Errorf("%q is not a 24-hour HH:MM time", f)
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out, nil
}

// occurrences returns each run time on the days around now, in loc.
func occurrences(runTimes []string, loc *time.Location, now time.Time) []time.Time {
	local := now.In(loc)
	var out []time.Time
	for _, dayOffset := range []int{-1, 0, 1} {
		day := local.AddDate(0, 0, dayOffset)
		for _, rt := range runTimes {
			var h, m int
			if _, err := fmt.Sscanf(rt, "%d:%d", &h, &m); err != nil {
				continue
			}
			out = append(out, time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// LatestDue returns the most recent run time in (now-grace, now], if any.
func LatestDue(runTimes []string, loc *time.Location, now time.Time, grace time.Duration) (time.Time, bool) {
	var due time.Time
	found := false
	for _, t := range occurrences(runTimes, loc, now) {
		if t.After(now.Add(-grace)) && !t.After(now) {
			due, found = t, true
		}
	}
	return due, found
}

// Next returns the first run time strictly after now, if any are set.
func Next(runTimes []string, loc *time.Location, now time.Time) (time.Time, bool) {
	for _, t := range occurrences(runTimes, loc, now) {
		if t.After(now) {
			return t, true
		}
	}
	return time.Time{}, false
}

type Scheduler struct {
	Store *storage.Store
	// Wake nudges the worker after a run is queued, so it starts at once
	// instead of on its next poll.
	Wake func()
}

// Run ticks every 30 seconds until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		s.tick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	teams, err := s.Store.TeamSchedules(ctx)
	if err != nil {
		log.Printf("scheduler: loading schedules: %v", err)
		return
	}
	for _, t := range teams {
		loc, err := time.LoadLocation(t.Timezone)
		if err != nil {
			log.Printf("scheduler: team %s has invalid timezone %q: %v", t.TeamID, t.Timezone, err)
			continue
		}
		due, ok := LatestDue(t.RunTimes, loc, now, Grace)
		if !ok {
			continue
		}
		queued, err := s.Store.EnqueueScheduled(ctx, t.TeamID, due.UTC())
		if err != nil {
			log.Printf("scheduler: queueing run for team %s: %v", t.TeamID, err)
			continue
		}
		if queued {
			log.Printf("scheduler: queued scheduled run for team %s at %s", t.TeamID, due.Format(time.RFC3339))
			if s.Wake != nil {
				s.Wake()
			}
		}
	}
}
