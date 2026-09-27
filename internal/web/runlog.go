package web

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/runlog"
	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

var logPageSizes = []int{50, 100, 200, 500}

const defaultLogPageSize = 100

type runLogData struct {
	Loc     *time.Location // the team's timezone, for timestamps
	RunID   string
	Live    bool // the run is still going: the log refreshes itself
	Follow  bool // showing the newest lines as they arrive
	Filter  string
	Q       string
	Size    int
	Sizes   []int
	Filters []option
	Entries []storage.LogEntry

	Total, Page, Pages, From, To        int
	FirstURL, PrevURL, NextURL, LastURL string
	SelfURL                             string // what the live refresh re-requests
}

var logFilterOptions = []option{
	{string(storage.LogFilterAll), "All lines"},
	{string(storage.LogFilterProblems), "Warnings and errors"},
	{string(storage.LogFilterErrors), "Errors only"},
}

func (d runLogData) url(page string) string {
	v := url.Values{}
	if d.Filter != string(storage.LogFilterAll) {
		v.Set("level", d.Filter)
	}
	if d.Q != "" {
		v.Set("q", d.Q)
	}
	v.Set("size", strconv.Itoa(d.Size))
	v.Set("page", page)
	return "/runs/" + d.RunID + "/log?" + v.Encode()
}

// loadRunLog reads one page of a run's log from the request's filters.
// While a run is live and the user hasn't paged, it follows the newest
// lines, like `tail -f`.
func (s *Server) loadRunLog(r *http.Request, run *storage.Run) (runLogData, error) {
	q := r.URL.Query()
	d := runLogData{RunID: run.ID, Sizes: logPageSizes, Filters: logFilterOptions,
		Filter: q.Get("level"), Q: strings.TrimSpace(q.Get("q")), Size: defaultLogPageSize,
		Live: run.Status == storage.RunQueued || run.Status == storage.RunRunning}
	if !storage.ValidLogFilter(d.Filter) {
		d.Filter = string(storage.LogFilterAll)
	}
	if len(d.Q) > 200 {
		d.Q = d.Q[:200]
	}
	if n, err := strconv.Atoi(q.Get("size")); err == nil && slices.Contains(logPageSizes, n) {
		d.Size = n
	}
	pageParam := q.Get("page")
	d.Follow = pageParam == "last" || (pageParam == "" && d.Live)
	d.Page, _ = strconv.Atoi(pageParam)
	d.Page = max(d.Page, 1)

	repo := sessionFrom(r).repo
	d.Loc = time.UTC
	if team, err := repo.Settings(r.Context()); err == nil {
		if loc, err := time.LoadLocation(team.Timezone); err == nil {
			d.Loc = loc
		}
	}
	lq := storage.LogQuery{Filter: storage.LogLevelFilter(d.Filter), Search: d.Q, Limit: d.Size, Offset: (d.Page - 1) * d.Size}
	entries, total, err := repo.RunLogs(r.Context(), run.ID, lq)
	if err != nil {
		return d, err
	}
	d.Pages = max((total+d.Size-1)/d.Size, 1)
	if d.Follow || d.Page > d.Pages {
		if d.Page != d.Pages {
			d.Page = d.Pages
			lq.Offset = (d.Page - 1) * d.Size
			if entries, total, err = repo.RunLogs(r.Context(), run.ID, lq); err != nil {
				return d, err
			}
		}
	}
	d.Entries, d.Total = entries, total
	if total > 0 {
		d.From = (d.Page-1)*d.Size + 1
		d.To = d.From + len(entries) - 1
	}
	if d.Page > 1 {
		d.FirstURL = d.url("1")
		d.PrevURL = d.url(strconv.Itoa(d.Page - 1))
	}
	if d.Page < d.Pages {
		d.NextURL = d.url(strconv.Itoa(d.Page + 1))
		d.LastURL = d.url("last")
	}
	if d.Follow {
		d.SelfURL = d.url("last")
	} else {
		d.SelfURL = d.url(strconv.Itoa(d.Page))
	}
	return d, nil
}

// runLog serves the log section as an htmx fragment: filtering, paging and
// the live refresh all re-render it.
func (s *Server) runLog(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadRun(w, r)
	if !ok {
		return
	}
	logData, err := s.loadRunLog(r, d.Run)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderFragment(w, r, "run", "logfrag", page{Data: logData})
}

// runLogDownload streams a run's whole log as a plain-text report.
func (s *Server) runLogDownload(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadRun(w, r)
	if !ok {
		return
	}
	repo := sessionFrom(r).repo
	team, err := repo.Settings(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	loc, err := time.LoadLocation(team.Timezone)
	if err != nil {
		loc = time.UTC
	}
	run := d.Run

	name := fmt.Sprintf("tdms-%s-run-%s-%s.log.txt", team.Name, run.ID[:8], run.CreatedAt.In(loc).Format("20060102-1504"))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	out := bufio.NewWriter(w)
	defer out.Flush()

	c := run.Counts
	fmt.Fprintf(out, "TDMS run log\n")
	fmt.Fprintf(out, "Team:      %s\nRun:       %s\nMode:      %s (%s)\nScope:     %s\nStatus:    %s\n",
		team.Name, run.ID, run.Mode, run.Trigger, scopeText(run.Scope), run.Status)
	fmt.Fprintf(out, "Created:   %s\n", run.CreatedAt.In(loc).Format("2006-01-02 15:04:05 MST"))
	if run.FinishedAt != nil {
		fmt.Fprintf(out, "Finished:  %s\n", run.FinishedAt.In(loc).Format("2006-01-02 15:04:05 MST"))
	}
	fmt.Fprintf(out, "Result:    %d checked, %d valid, %d invalid, %d generated, %d errors\n",
		c.Total, c.Valid, c.Invalid, c.Generated, c.Errors)
	if run.SyncError != nil {
		fmt.Fprintf(out, "Sync error: %s\n", *run.SyncError)
	}
	if run.Error != nil {
		fmt.Fprintf(out, "Run error: %s\n", *run.Error)
	}
	fmt.Fprintf(out, "%s\n", strings.Repeat("-", 100))

	lines := 0
	err = repo.EachRunLog(r.Context(), run.ID, func(e storage.LogEntry) error {
		lines++
		_, werr := fmt.Fprintln(out, runlog.Format(e, loc))
		return werr
	})
	if err != nil && !errors.Is(err, r.Context().Err()) {
		fmt.Fprintf(out, "\n[log truncated: %v]\n", err)
		return
	}
	if lines == 0 {
		fmt.Fprintln(out, "(no log lines — the run predates activity logging, or its log was pruned after the retention period)")
	}
}

func scopeText(s storage.RunScope) string {
	switch {
	case len(s.SlotIDs) > 0:
		return fmt.Sprintf("%d selected test case(s)", len(s.SlotIDs))
	case s.Environment != "":
		return "environment " + s.Environment
	default:
		return "whole folder, all environments"
	}
}
