package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/scheduler"
	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

// notices are the fixed confirmation messages a redirect can ask for via
// ?notice=, so no user-supplied text is ever echoed from a URL.
var notices = map[string]string{
	"password":     "Your password was changed.",
	"integration":  "Integration settings saved.",
	"environments": "Environments saved.",
	"schedule":     "Run schedule saved.",
	"recipients":   "Report recipients saved.",
}

func notice(r *http.Request) string { return notices[r.URL.Query().Get("notice")] }

type dashboardData struct {
	Stats    []storage.EnvStats
	Runs     []storage.Run
	Problems []storage.RunItem
	NextRun  *time.Time
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	repo := sessionFrom(r).repo
	ctx := r.Context()
	team, err := repo.Settings(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var d dashboardData
	if d.Stats, err = repo.EnvStats(ctx); err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Runs, err = repo.Runs(ctx, 5); err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Problems, err = repo.RecentProblems(ctx, 10); err != nil {
		s.serverError(w, r, err)
		return
	}
	if loc, err := time.LoadLocation(team.Timezone); err == nil {
		if next, ok := scheduler.Next(team.RunTimes, loc, time.Now()); ok {
			d.NextRun = &next
		}
	}
	s.render(w, r, http.StatusOK, "dashboard", page{Title: "Dashboard", Active: "dashboard", Team: team, Notice: notice(r), Data: d})
}

// testCaseAction queues a run over the selected slots: validate (report
// only), heal (fix only what's invalid) or regenerate (force fresh PNRs).
func (s *Server) testCaseAction(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	modes := map[string]storage.RunMode{
		"validate": storage.ModeCheck, "heal": storage.ModeHeal, "regenerate": storage.ModeRegenerate,
	}
	mode, ok := modes[r.FormValue("action")]
	if !ok {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	var requested []string
	for _, id := range r.Form["slot_id"] {
		if uuidPattern.MatchString(id) {
			requested = append(requested, id)
		}
	}
	owned, err := sess.repo.OwnedSlotIDs(r.Context(), requested)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(owned) == 0 {
		// The page keeps the buttons disabled until something is selected,
		// so this only answers a request made without the UI.
		http.Error(w, "Select at least one test case with a TDMS block first.", http.StatusBadRequest)
		return
	}
	s.queueRun(w, r, mode, storage.RunScope{SlotIDs: owned})
}

type newScanData struct {
	Environments []storage.Environment
}

func (s *Server) newScan(w http.ResponseWriter, r *http.Request) {
	envs, err := sessionFrom(r).repo.Environments(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "scan_new", page{Title: "Custom scan", Active: "scans", Data: newScanData{envs}})
}

// startScan queues a folder- or environment-wide custom scan.
func (s *Server) startScan(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	mode := storage.RunMode(r.FormValue("mode"))
	if mode != storage.ModeCheck && mode != storage.ModeHeal {
		http.Error(w, "mode must be check or heal", http.StatusBadRequest)
		return
	}
	env := r.FormValue("environment")
	if env != "" {
		envs, err := sess.repo.Environments(r.Context())
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		found := false
		for _, e := range envs {
			found = found || e.Name == env
		}
		if !found {
			http.Error(w, "unknown environment", http.StatusBadRequest)
			return
		}
	}
	s.queueRun(w, r, mode, storage.RunScope{Environment: env})
}

func (s *Server) queueRun(w http.ResponseWriter, r *http.Request, mode storage.RunMode, scope storage.RunScope) {
	sess := sessionFrom(r)
	runID, err := sess.repo.CreateRun(r.Context(), mode, scope, &sess.user.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.wake()
	redirect(w, r, "/runs/"+runID)
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	runs, err := sessionFrom(r).repo.Runs(r.Context(), 100)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "runs", page{Title: "Runs", Active: "runs", Data: runs})
}

type runData struct {
	Run   *storage.Run
	Items []storage.RunItem
	Live  bool // still queued or running: the page polls for updates
	Log   runLogData
}

func (s *Server) loadRun(w http.ResponseWriter, r *http.Request) (*runData, bool) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return nil, false
	}
	repo := sessionFrom(r).repo
	run, err := repo.Run(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	items, err := repo.Items(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	live := run.Status == storage.RunQueued || run.Status == storage.RunRunning
	return &runData{Run: run, Items: items, Live: live}, true
}

func (s *Server) runPage(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadRun(w, r)
	if !ok {
		return
	}
	logData, err := s.loadRunLog(r, d.Run)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Log = logData
	s.render(w, r, http.StatusOK, "run", page{Title: "Run " + d.Run.ID[:8], Active: "runs", Data: d})
}

// runLive is the htmx-polled fragment of a run's page.
func (s *Server) runLive(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadRun(w, r)
	if !ok {
		return
	}
	s.renderFragment(w, r, "run", "live", page{Data: d})
}

type reportData struct {
	From, To  string
	Instances []storage.GeneratedInstance
}

// generationReport is the test data creation report over a date range,
// interpreted in the team's timezone; To is inclusive.
func (s *Server) generationReport(w http.ResponseWriter, r *http.Request) {
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
	today := time.Now().In(loc)
	from := parseDate(r.URL.Query().Get("from"), loc, today.AddDate(0, 0, -30))
	to := parseDate(r.URL.Query().Get("to"), loc, today)
	if to.Before(from) {
		from, to = to, from
	}
	instances, err := repo.GeneratedInstances(r.Context(), dayStart(from), dayStart(to).AddDate(0, 0, 1))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "report", page{Title: "Test data creation report", Active: "report", Team: team,
		Data: reportData{From: from.Format("2006-01-02"), To: to.Format("2006-01-02"), Instances: instances}})
}

func parseDate(v string, loc *time.Location, fallback time.Time) time.Time {
	if t, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
		return t
	}
	return fallback
}

func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
