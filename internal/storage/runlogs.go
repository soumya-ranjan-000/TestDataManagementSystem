package storage

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type LogLevel string

const (
	LogInfo  LogLevel = "info"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

// LogEntry is one line of a run's activity log.
type LogEntry struct {
	ID          int64     `json:"-"`
	At          time.Time `json:"at"`
	Level       LogLevel  `json:"level"`
	TestCaseKey string    `json:"test_case_key,omitempty"`
	Environment string    `json:"environment,omitempty"`
	Message     string    `json:"message"`
}

// AppendRunLogs writes a batch of log lines for one of this team's runs in a
// single statement, keeping their order. Lines for another team's run are
// silently dropped.
func (r *TeamRepo) AppendRunLogs(ctx context.Context, runID string, entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	payload, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO scan_run_logs (run_id, at, level, test_case_key, environment, message)
		SELECT run.id, (e.v->>'at')::timestamptz, e.v->>'level',
		       NULLIF(e.v->>'test_case_key', ''), NULLIF(e.v->>'environment', ''), e.v->>'message'
		FROM scan_runs run, jsonb_array_elements($2::jsonb) WITH ORDINALITY AS e(v, n)
		WHERE run.id = $1 AND run.team_id = $3
		ORDER BY e.n`, runID, payload, r.teamID)
	return err
}

// LogLevelFilter narrows a log listing.
type LogLevelFilter string

const (
	LogFilterAll      LogLevelFilter = "all"
	LogFilterProblems LogLevelFilter = "problems" // warnings and errors
	LogFilterErrors   LogLevelFilter = "errors"
)

var logFilterLevels = map[LogLevelFilter][]string{
	LogFilterAll:      {"info", "warn", "error"},
	LogFilterProblems: {"warn", "error"},
	LogFilterErrors:   {"error"},
}

func ValidLogFilter(f string) bool { _, ok := logFilterLevels[LogLevelFilter(f)]; return ok }

type LogQuery struct {
	Filter LogLevelFilter
	Search string // matches the test case key or the message
	Limit  int
	Offset int
}

// RunLogs returns one page of a run's log in order, and the total number of
// matching lines.
func (r *TeamRepo) RunLogs(ctx context.Context, runID string, q LogQuery) ([]LogEntry, int, error) {
	levels, ok := logFilterLevels[q.Filter]
	if !ok {
		levels = logFilterLevels[LogFilterAll]
	}
	search := strings.TrimSpace(q.Search)
	where := `
		FROM scan_run_logs l JOIN scan_runs run ON run.id = l.run_id
		WHERE l.run_id = $1 AND run.team_id = $2 AND l.level = ANY($3)
		  AND ($4 = '' OR l.test_case_key ILIKE $5 ESCAPE '\' OR l.message ILIKE $5 ESCAPE '\')`
	args := []any{runID, r.teamID, levels, search, "%" + escapeLike(search) + "%"}

	var total int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.pool.Query(ctx, `
		SELECT l.id, l.at, l.level, COALESCE(l.test_case_key, ''), COALESCE(l.environment, ''), l.message `+where+`
		ORDER BY l.id LIMIT $6 OFFSET $7`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	entries, err := pgx.CollectRows(rows, scanLogEntry)
	return entries, total, err
}

// EachRunLog streams a run's whole log in order, for downloading.
func (r *TeamRepo) EachRunLog(ctx context.Context, runID string, fn func(LogEntry) error) error {
	rows, err := r.pool.Query(ctx, `
		SELECT l.id, l.at, l.level, COALESCE(l.test_case_key, ''), COALESCE(l.environment, ''), l.message
		FROM scan_run_logs l JOIN scan_runs run ON run.id = l.run_id
		WHERE l.run_id = $1 AND run.team_id = $2 ORDER BY l.id`, runID, r.teamID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanLogEntry(rows)
		if err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
}

func scanLogEntry(row pgx.CollectableRow) (LogEntry, error) {
	var e LogEntry
	var level string
	err := row.Scan(&e.ID, &e.At, &level, &e.TestCaseKey, &e.Environment, &e.Message)
	e.Level = LogLevel(level)
	return e, err
}

// PurgeRunLogs deletes log lines older than the retention period. Run
// summaries and items are kept.
func (s *Store) PurgeRunLogs(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM scan_run_logs WHERE at < now() - make_interval(secs => $1)`, olderThan.Seconds())
	return tag.RowsAffected(), err
}
