package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the whole database, for the operations that legitimately span
// teams: operator setup, the worker's queue, the scheduler, and login
// (which happens before a team is known). Everything a team's own users can
// reach goes through a TeamRepo instead.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Team returns a repo that can only see and change teamID's data.
func (s *Store) Team(teamID string) *TeamRepo {
	return &TeamRepo{pool: s.pool, teamID: teamID}
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		(constraint == "" || pgErr.ConstraintName == constraint)
}

// --- operator ---------------------------------------------------------------

// AddEnvironment registers an environment and the PSS it talks to, or
// updates its URL if it already exists.
func (s *Store) AddEnvironment(ctx context.Context, name, pssBaseURL string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO environments (name, pss_base_url) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET pss_base_url = EXCLUDED.pss_base_url`,
		name, strings.TrimRight(pssBaseURL, "/"))
	return err
}

func (s *Store) AllEnvironments(ctx context.Context) ([]Environment, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, pss_base_url FROM environments ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Environment, error) {
		var e Environment
		err := row.Scan(&e.ID, &e.Name, &e.PSSBaseURL)
		return e, err
	})
}

// CreateTeam creates a team bound to its operator-assigned QMetry project,
// enrolled in the named environments, with its first admin user — all in
// one transaction so a half-created team never exists.
func (s *Store) CreateTeam(ctx context.Context, name, projectKey string, envNames []string, admin User) (teamID string, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx,
		`INSERT INTO teams (name, tcm_project_key) VALUES ($1, $2) RETURNING id`,
		name, projectKey,
	).Scan(&teamID)
	if isUniqueViolation(err, "teams_name_key") {
		return "", fmt.Errorf("a team named %q already exists", name)
	}
	if err != nil {
		return "", err
	}

	for _, env := range envNames {
		tag, err := tx.Exec(ctx, `
			INSERT INTO team_environments (team_id, environment_id)
			SELECT $1, id FROM environments WHERE name = $2`, teamID, env)
		if err != nil {
			return "", err
		}
		if tag.RowsAffected() == 0 {
			return "", fmt.Errorf("environment %q does not exist (add it with add-environment first)", env)
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO users (team_id, email, name, password_hash, role, must_change_password)
		VALUES ($1, $2, $3, $4, 'admin', true)`,
		teamID, strings.ToLower(admin.Email), admin.Name, admin.PasswordHash)
	if isUniqueViolation(err, "users_email_key") {
		return "", fmt.Errorf("a user with email %q already exists", admin.Email)
	}
	if err != nil {
		return "", err
	}
	return teamID, tx.Commit(ctx)
}

func (s *Store) TeamIDByName(ctx context.Context, name string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT id FROM teams WHERE name = $1`, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// --- worker -----------------------------------------------------------------

// FailInterruptedRuns marks runs left `running` by a crash or restart as
// failed, freeing each team's single running-run position.
func (s *Store) FailInterruptedRuns(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE scan_runs SET status = 'failed', error = 'interrupted: the server restarted mid-run',
		       finished_at = now()
		WHERE status = 'running'`)
	return tag.RowsAffected(), err
}

// SweepStaleGenerating clears instances stuck in GENERATING (the process
// died mid-generation), which would otherwise block their slot forever.
// One that got as far as a booking keeps its PNR in the trail as RETIRED
// generation_failed; one that never booked is simply removed.
func (s *Store) SweepStaleGenerating(ctx context.Context, olderThan time.Duration) (retired, deleted int64, err error) {
	cutoff := time.Now().Add(-olderThan)
	tag, err := s.pool.Exec(ctx, `
		UPDATE instances SET state = 'RETIRED', retire_reason = 'generation_failed', retired_at = now()
		WHERE state = 'GENERATING' AND pnr IS NOT NULL AND created_at < $1`, cutoff)
	if err != nil {
		return 0, 0, err
	}
	retired = tag.RowsAffected()
	tag, err = s.pool.Exec(ctx, `
		DELETE FROM instances WHERE state = 'GENERATING' AND pnr IS NULL AND created_at < $1`, cutoff)
	if err != nil {
		return retired, 0, err
	}
	return retired, tag.RowsAffected(), nil
}

// NextQueuedRun returns the oldest queued run whose team isn't already
// running one, or nil if there is none.
func (s *Store) NextQueuedRun(ctx context.Context) (*Run, error) {
	var id, teamID string
	err := s.pool.QueryRow(ctx, `
		SELECT q.id, q.team_id FROM scan_runs q
		WHERE q.status = 'queued'
		  AND NOT EXISTS (SELECT 1 FROM scan_runs r WHERE r.team_id = q.team_id AND r.status = 'running')
		ORDER BY q.created_at
		LIMIT 1`).Scan(&id, &teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Team(teamID).Run(ctx, id)
}

// StartRun moves a queued run to running. The one_running_run_per_team
// index is the real guard: a second concurrent start for the same team
// fails with ErrRunInProgress rather than racing.
func (s *Store) StartRun(ctx context.Context, runID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE scan_runs SET status = 'running', started_at = now()
		WHERE id = $1 AND status = 'queued'`, runID)
	if isUniqueViolation(err, "one_running_run_per_team") {
		return ErrRunInProgress
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("run %s is not queued", runID)
	}
	return nil
}

// --- scheduler --------------------------------------------------------------

type TeamSchedule struct {
	TeamID   string
	RunTimes []string
	Timezone string
}

func (s *Store) TeamSchedules(ctx context.Context) ([]TeamSchedule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_times, timezone FROM teams WHERE cardinality(run_times) > 0`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TeamSchedule, error) {
		var t TeamSchedule
		err := row.Scan(&t.TeamID, &t.RunTimes, &t.Timezone)
		return t, err
	})
}

// EnqueueScheduled queues a heal run for one scheduled slot. The
// (team_id, scheduled_for) unique key makes it idempotent: however many
// times the scheduler ticks over the same due time, one run is queued.
func (s *Store) EnqueueScheduled(ctx context.Context, teamID string, scheduledFor time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO scan_runs (team_id, trigger, mode, scheduled_for)
		VALUES ($1, 'scheduled', 'heal', $2)
		ON CONFLICT (team_id, scheduled_for) DO NOTHING`, teamID, scheduledFor)
	return tag.RowsAffected() == 1, err
}
