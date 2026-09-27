package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// --- dashboard & listings ---------------------------------------------------

type EnvStats struct {
	Environment string
	Valid       int
	Generating  int
	Empty       int
	Orphaned    int
}

// EnvStats counts the team's slots per environment by the state of their
// live instance.
func (r *TeamRepo) EnvStats(ctx context.Context) ([]EnvStats, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT s.environment,
		       count(*) FILTER (WHERE s.orphaned_at IS NULL AND i.state = 'VALID'),
		       count(*) FILTER (WHERE s.orphaned_at IS NULL AND i.state = 'GENERATING'),
		       count(*) FILTER (WHERE s.orphaned_at IS NULL AND i.id IS NULL),
		       count(*) FILTER (WHERE s.orphaned_at IS NOT NULL)
		FROM slots s
		LEFT JOIN instances i ON i.slot_id = s.id AND i.state <> 'RETIRED'
		WHERE s.team_id = $1
		GROUP BY s.environment ORDER BY s.environment`, r.teamID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (EnvStats, error) {
		var e EnvStats
		err := row.Scan(&e.Environment, &e.Valid, &e.Generating, &e.Empty, &e.Orphaned)
		return e, err
	})
}

// OwnedSlotIDs filters ids down to those that are this team's live slots,
// so a tampered form can't queue work against another team's data.
func (r *TeamRepo) OwnedSlotIDs(ctx context.Context, ids []string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text FROM slots
		WHERE team_id = $1 AND orphaned_at IS NULL AND id::text = ANY($2)`, r.teamID, nonNilStrings(ids))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Runs lists the team's most recent runs, newest first.
func (r *TeamRepo) Runs(ctx context.Context, limit int) ([]Run, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+runColumns+` FROM scan_runs WHERE team_id = $1 ORDER BY created_at DESC LIMIT $2`,
		r.teamID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Run, error) {
		run, err := scanRun(row)
		if err != nil {
			return Run{}, err
		}
		return *run, nil
	})
}

// RecentProblems returns error items from the team's latest runs.
func (r *TeamRepo) RecentProblems(ctx context.Context, limit int) ([]RunItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT i.id, i.slot_id, i.test_case_key, i.environment, i.outcome, i.old_pnr, i.new_pnr,
		       i.failed_rule, i.error, i.created_at
		FROM scan_run_items i JOIN scan_runs r ON r.id = i.run_id
		WHERE r.team_id = $1 AND i.outcome IN ('generation_failed', 'check_error', 'block_error')
		ORDER BY i.created_at DESC LIMIT $2`, r.teamID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (RunItem, error) {
		var it RunItem
		var outcome string
		err := row.Scan(&it.ID, &it.SlotID, &it.TestCaseKey, &it.Environment, &outcome, &it.OldPNR, &it.NewPNR,
			&it.FailedRule, &it.Error, &it.CreatedAt)
		it.Outcome = Outcome(outcome)
		return it, err
	})
}

// GeneratedInstances is the test data creation report: every PNR the team's
// runs created in [from, to), with what became of it.
func (r *TeamRepo) GeneratedInstances(ctx context.Context, from, to time.Time) ([]GeneratedInstance, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT i.id, s.test_case_key, s.environment, COALESCE(i.pnr, ''), i.state, i.retire_reason,
		       i.created_by_run_id::text, i.created_at, i.retired_at
		FROM instances i JOIN slots s ON s.id = i.slot_id
		WHERE s.team_id = $1 AND i.created_at >= $2 AND i.created_at < $3 AND i.pnr IS NOT NULL
		ORDER BY i.created_at DESC`, r.teamID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (GeneratedInstance, error) {
		var g GeneratedInstance
		err := row.Scan(&g.InstanceID, &g.TestCaseKey, &g.Environment, &g.PNR, &g.State, &g.RetireReason,
			&g.RunID, &g.CreatedAt, &g.RetiredAt)
		return g, err
	})
}

// --- settings ---------------------------------------------------------------

// UpdateSchedule sets the team's daily run times and timezone.
func (r *TeamRepo) UpdateSchedule(ctx context.Context, runTimes []string, timezone string) error {
	return r.expectOne(r.pool.Exec(ctx, `
		UPDATE teams SET run_times = $1, timezone = $2, updated_at = now() WHERE id = $3`,
		nonNilStrings(runTimes), timezone, r.teamID))
}

// AvailableEnvironments lists every operator-defined environment, for an
// admin choosing which ones the team uses.
func (r *TeamRepo) AvailableEnvironments(ctx context.Context) ([]Environment, error) {
	return (&Store{pool: r.pool}).AllEnvironments(ctx)
}

// SetEnvironments replaces the team's enrolled environments. Unknown ids are
// ignored, so only operator-defined environments can be selected.
func (r *TeamRepo) SetEnvironments(ctx context.Context, envIDs []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM team_environments WHERE team_id = $1`, r.teamID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO team_environments (team_id, environment_id)
		SELECT $1, id FROM environments WHERE id::text = ANY($2)`, r.teamID, nonNilStrings(envIDs)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- report recipients ------------------------------------------------------

func (r *TeamRepo) Recipients(ctx context.Context) ([]Recipient, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, email, on_scan, on_generation, on_error FROM team_recipients
		WHERE team_id = $1 ORDER BY email`, r.teamID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Recipient, error) {
		var rc Recipient
		err := row.Scan(&rc.ID, &rc.Email, &rc.OnScan, &rc.OnGeneration, &rc.OnError)
		return rc, err
	})
}

func (r *TeamRepo) AddRecipient(ctx context.Context, rc Recipient) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO team_recipients (team_id, email, on_scan, on_generation, on_error)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (team_id, email) DO UPDATE
		SET on_scan = EXCLUDED.on_scan, on_generation = EXCLUDED.on_generation, on_error = EXCLUDED.on_error`,
		r.teamID, strings.ToLower(rc.Email), rc.OnScan, rc.OnGeneration, rc.OnError)
	return err
}

func (r *TeamRepo) UpdateRecipient(ctx context.Context, rc Recipient) error {
	return r.expectOne(r.pool.Exec(ctx, `
		UPDATE team_recipients SET on_scan = $1, on_generation = $2, on_error = $3
		WHERE id = $4 AND team_id = $5`, rc.OnScan, rc.OnGeneration, rc.OnError, rc.ID, r.teamID))
}

func (r *TeamRepo) DeleteRecipient(ctx context.Context, id string) error {
	return r.expectOne(r.pool.Exec(ctx,
		`DELETE FROM team_recipients WHERE id = $1 AND team_id = $2`, id, r.teamID))
}

// --- members ----------------------------------------------------------------

var ErrLastAdmin = errors.New("a team must keep at least one admin")

func (r *TeamRepo) Members(ctx context.Context) ([]User, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+userColumns+` FROM users WHERE team_id = $1 ORDER BY email`, r.teamID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (User, error) {
		u, err := scanUser(row)
		if err != nil {
			return User{}, err
		}
		return *u, nil
	})
}

// CreateMember adds a user to this team; they must change the temporary
// password at first login.
func (r *TeamRepo) CreateMember(ctx context.Context, email, name, role, passwordHash string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO users (team_id, email, name, password_hash, role, must_change_password)
		VALUES ($1, $2, $3, $4, $5, true)`,
		r.teamID, strings.ToLower(strings.TrimSpace(email)), name, passwordHash, role)
	if isUniqueViolation(err, "users_email_key") {
		return fmt.Errorf("a user with email %q already exists", email)
	}
	return err
}

// SetMemberRole changes a member's role, refusing to demote the team's last
// admin. The admin count is checked under a row lock so two concurrent
// demotions can't both pass.
func (r *TeamRepo) SetMemberRole(ctx context.Context, userID, role string) error {
	return r.withAdminGuard(ctx, userID, role != RoleAdmin, func(tx pgx.Tx) error {
		return r.expectOne(tx.Exec(ctx,
			`UPDATE users SET role = $1 WHERE id = $2 AND team_id = $3`, role, userID, r.teamID))
	})
}

// DeleteMember removes a member (their sessions go with them via cascade),
// refusing to remove the team's last admin.
func (r *TeamRepo) DeleteMember(ctx context.Context, userID string) error {
	return r.withAdminGuard(ctx, userID, true, func(tx pgx.Tx) error {
		return r.expectOne(tx.Exec(ctx,
			`DELETE FROM users WHERE id = $1 AND team_id = $2`, userID, r.teamID))
	})
}

// ResetMemberPassword sets a new temporary password and signs the member out
// everywhere.
func (r *TeamRepo) ResetMemberPassword(ctx context.Context, userID, passwordHash string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := r.expectOne(tx.Exec(ctx, `
		UPDATE users SET password_hash = $1, must_change_password = true
		WHERE id = $2 AND team_id = $3`, passwordHash, userID, r.teamID)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// withAdminGuard runs change in a transaction that locks the team's admin
// rows. When losesAdmin is true and userID is currently an admin, the change
// is refused if it would leave the team with no admin.
func (r *TeamRepo) withAdminGuard(ctx context.Context, userID string, losesAdmin bool, change func(pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`SELECT id::text FROM users WHERE team_id = $1 AND role = 'admin' FOR UPDATE`, r.teamID)
	if err != nil {
		return err
	}
	admins, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	isAdmin := false
	for _, id := range admins {
		if id == userID {
			isAdmin = true
		}
	}
	if losesAdmin && isAdmin && len(admins) == 1 {
		return ErrLastAdmin
	}
	if err := change(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
