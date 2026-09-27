package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soumya-ranjan-000/tdms/internal/model"
)

// TeamRepo is bound to one team at construction and every query it runs
// filters on that team — directly, or by joining through slots/scan_runs.
// It is the only data access the web layer and the scan pipeline get, so a
// query that forgets the team can't be written against it. A row that
// belongs to another team is reported as ErrNotFound, never as forbidden.
type TeamRepo struct {
	pool   *pgxpool.Pool
	teamID string
}

func (r *TeamRepo) TeamID() string { return r.teamID }

// --- team settings ----------------------------------------------------------

func (r *TeamRepo) Settings(ctx context.Context) (*Team, error) {
	var t Team
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, tcm_provider, tcm_project_key, tcm_custom_field_id, tcm_folder_path,
		       run_times, timezone
		FROM teams WHERE id = $1`, r.teamID,
	).Scan(&t.ID, &t.Name, &t.TCMProvider, &t.TCMProjectKey, &t.TCMCustomFieldID, &t.TCMFolderPath,
		&t.RunTimes, &t.Timezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

// UpdateIntegration sets the admin-editable parts of the test management
// integration. The project key is deliberately not settable here: it is
// operator-assigned at team creation.
func (r *TeamRepo) UpdateIntegration(ctx context.Context, customFieldID, folderPath string) error {
	return r.expectOne(r.pool.Exec(ctx, `
		UPDATE teams SET tcm_custom_field_id = $1, tcm_folder_path = $2, updated_at = now()
		WHERE id = $3`, customFieldID, folderPath, r.teamID))
}

// Environments returns the environments this team is enrolled in.
func (r *TeamRepo) Environments(ctx context.Context) ([]Environment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT e.id, e.name, e.pss_base_url
		FROM environments e JOIN team_environments te ON te.environment_id = e.id
		WHERE te.team_id = $1 ORDER BY e.name`, r.teamID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Environment, error) {
		var e Environment
		err := row.Scan(&e.ID, &e.Name, &e.PSSBaseURL)
		return e, err
	})
}

// --- slots ------------------------------------------------------------------

// UpsertSlot creates or refreshes the slot for one test case in one
// environment. A slot is rewritten when its block hash changed, or to
// recover it from being orphaned or holding a block error; otherwise only
// the test management version is refreshed. changed reports a new slot or
// a new block.
func (r *TeamRepo) UpsertSlot(ctx context.Context, u SlotUpsert) (slotID string, changed bool, err error) {
	blockJSON, err := json.Marshal(u.Block)
	if err != nil {
		return "", false, err
	}
	var existingHash string
	err = r.pool.QueryRow(ctx, `
		SELECT id, block_hash FROM slots
		WHERE team_id = $1 AND test_case_key = $2 AND environment = $3`,
		r.teamID, u.TestCaseKey, u.Environment,
	).Scan(&slotID, &existingHash)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		err = r.pool.QueryRow(ctx, `
			INSERT INTO slots (team_id, test_case_key, tcm_test_case_id, tcm_version, class, environment, block, block_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
			r.teamID, u.TestCaseKey, u.TCMTestCaseID, u.TCMVersion, string(u.Class), u.Environment, blockJSON, u.BlockHash,
		).Scan(&slotID)
		return slotID, true, err
	case err != nil:
		return "", false, err
	}

	changed = existingHash != u.BlockHash
	_, err = r.pool.Exec(ctx, `
		UPDATE slots SET
			tcm_test_case_id = $1, tcm_version = $2,
			block = CASE WHEN $3 THEN $4::jsonb ELSE block END,
			block_hash = $5, class = $6,
			block_error = NULL, orphaned_at = NULL,
			updated_at = CASE WHEN $3 THEN now() ELSE updated_at END
		WHERE id = $7 AND team_id = $8`,
		u.TCMTestCaseID, u.TCMVersion, changed, blockJSON, u.BlockHash, string(u.Class), slotID, r.teamID)
	return slotID, changed, err
}

// MarkBlockError records that a test case's latest block failed to parse or
// validate, keeping the slot's last good block in place. found is false when
// no slot exists yet (the very first sync saw a bad block).
func (r *TeamRepo) MarkBlockError(ctx context.Context, testCaseKey, environment, msg string) (slotID string, found bool, err error) {
	err = r.pool.QueryRow(ctx, `
		UPDATE slots SET block_error = $1, updated_at = now()
		WHERE team_id = $2 AND test_case_key = $3 AND environment = $4
		RETURNING id`, msg, r.teamID, testCaseKey, environment,
	).Scan(&slotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return slotID, err == nil, err
}

type OrphanedSlot struct {
	ID          string
	TestCaseKey string
	Environment string
}

// OrphanMissing marks every live slot whose test case is not in present as
// orphaned: the test case left the folder or lost its TDMS block, so
// generation stops, but its history is kept. Callers must only pass the
// result of a complete listing of the whole configured folder.
func (r *TeamRepo) OrphanMissing(ctx context.Context, present []string) ([]OrphanedSlot, error) {
	rows, err := r.pool.Query(ctx, `
		UPDATE slots SET orphaned_at = now(), updated_at = now()
		WHERE team_id = $1 AND orphaned_at IS NULL AND NOT (test_case_key = ANY($2))
		RETURNING id, test_case_key, environment`, r.teamID, present)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (OrphanedSlot, error) {
		var o OrphanedSlot
		err := row.Scan(&o.ID, &o.TestCaseKey, &o.Environment)
		return o, err
	})
}

const slotColumns = `s.id, s.team_id, s.test_case_key, s.tcm_test_case_id, s.tcm_version, s.class,
	s.environment, s.block, s.block_hash, s.block_error, s.orphaned_at`

func scanSlot(row pgx.Row, extra ...any) (model.Slot, error) {
	var s model.Slot
	var class string
	var blockJSON []byte
	dest := append([]any{&s.ID, &s.TeamID, &s.TestCaseKey, &s.TCMTestCaseID, &s.TCMVersion, &class,
		&s.Environment, &blockJSON, &s.BlockHash, &s.BlockError, &s.OrphanedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return s, err
	}
	s.Class = model.Class(class)
	if err := json.Unmarshal(blockJSON, &s.Block); err != nil {
		return s, fmt.Errorf("decoding stored block for %s: %w", s.TestCaseKey, err)
	}
	return s, nil
}

// SlotsInScope returns the live (non-orphaned) slots a run should evaluate,
// limited to environments the team is still enrolled in. Slot ids in the
// scope that belong to another team simply don't match.
func (r *TeamRepo) SlotsInScope(ctx context.Context, scope RunScope) ([]model.Slot, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+slotColumns+`
		FROM slots s
		JOIN environments e ON e.name = s.environment
		JOIN team_environments te ON te.environment_id = e.id AND te.team_id = s.team_id
		WHERE s.team_id = $1 AND s.orphaned_at IS NULL
		  AND ($2 = '' OR s.environment = $2)
		  AND (cardinality($3::uuid[]) = 0 OR s.id = ANY($3::uuid[]))
		ORDER BY s.test_case_key, s.environment`,
		r.teamID, scope.Environment, nonNilStrings(scope.SlotIDs))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Slot, error) {
		return scanSlot(row)
	})
}

// TouchSlot records the outcome of the latest check on a slot.
func (r *TeamRepo) TouchSlot(ctx context.Context, slotID string, outcome Outcome) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE slots SET last_checked_at = now(), last_outcome = $1
		WHERE id = $2 AND team_id = $3`, string(outcome), slotID, r.teamID)
	return err
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// --- instances --------------------------------------------------------------

// CurrentInstance returns the slot's live (non-retired) instance, or nil if
// the slot is empty.
func (r *TeamRepo) CurrentInstance(ctx context.Context, slotID string) (*model.Instance, error) {
	var inst model.Instance
	var pnr, blockHash *string
	var state string
	var testData []byte
	err := r.pool.QueryRow(ctx, `
		SELECT i.id, i.slot_id, i.pnr, i.state, i.block_hash, i.test_data, i.created_at
		FROM instances i JOIN slots s ON s.id = i.slot_id
		WHERE i.slot_id = $1 AND s.team_id = $2 AND i.state <> 'RETIRED'`,
		slotID, r.teamID,
	).Scan(&inst.ID, &inst.SlotID, &pnr, &state, &blockHash, &testData, &inst.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	inst.State = model.InstanceState(state)
	if pnr != nil {
		inst.PNR = *pnr
	}
	if blockHash != nil {
		inst.BlockHash = *blockHash
	}
	if len(testData) > 0 {
		_ = json.Unmarshal(testData, &inst.TestData)
	}
	return &inst, nil
}

// BeginGeneration records the "EmptySlot -> Generating" transition. The
// one_live_instance_per_slot index turns a concurrent second generation for
// the same slot into ErrAlreadyGenerating instead of a duplicate PNR.
func (r *TeamRepo) BeginGeneration(ctx context.Context, slotID, runID, blockHash string) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO instances (slot_id, state, created_by_run_id, block_hash)
		SELECT id, 'GENERATING', $2, $3 FROM slots WHERE id = $1 AND team_id = $4
		RETURNING id`, slotID, runID, blockHash, r.teamID,
	).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", ErrNotFound
	case isUniqueViolation(err, "one_live_instance_per_slot"):
		return "", ErrAlreadyGenerating
	}
	return id, err
}

// RecordBookedPNR stores the PNR and its test data the moment PSS creates
// the booking, so a failure in a later generation step can't lose them.
func (r *TeamRepo) RecordBookedPNR(ctx context.Context, instanceID, pnr string, data []model.TestDatum) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return r.expectOne(r.pool.Exec(ctx, `
		UPDATE instances SET pnr = $1, test_data = $2
		FROM slots s WHERE instances.id = $3 AND s.id = instances.slot_id AND s.team_id = $4
		  AND instances.state = 'GENERATING'`, pnr, payload, instanceID, r.teamID))
}

// CompleteGeneration records "Generating -> Valid".
func (r *TeamRepo) CompleteGeneration(ctx context.Context, instanceID string) error {
	return r.expectOne(r.pool.Exec(ctx, `
		UPDATE instances SET state = 'VALID'
		FROM slots s WHERE instances.id = $1 AND s.id = instances.slot_id AND s.team_id = $2
		  AND instances.state = 'GENERATING' AND instances.pnr IS NOT NULL`, instanceID, r.teamID))
}

// FailGeneration records a failed generation. If a booking was already made
// its PNR stays in the trail as RETIRED generation_failed; otherwise the row
// is removed ("Generating -> EmptySlot"). Either way the slot is free.
func (r *TeamRepo) FailGeneration(ctx context.Context, instanceID string, bookedPNR string) error {
	if bookedPNR != "" {
		return r.expectOne(r.pool.Exec(ctx, `
			UPDATE instances SET state = 'RETIRED', retire_reason = 'generation_failed', retired_at = now(),
			       pnr = COALESCE(instances.pnr, $1)
			FROM slots s WHERE instances.id = $2 AND s.id = instances.slot_id AND s.team_id = $3
			  AND instances.state = 'GENERATING'`, bookedPNR, instanceID, r.teamID))
	}
	return r.expectOne(r.pool.Exec(ctx, `
		DELETE FROM instances USING slots s
		WHERE instances.id = $1 AND s.id = instances.slot_id AND s.team_id = $2
		  AND instances.state = 'GENERATING'`, instanceID, r.teamID))
}

// RetireInstance moves a VALID instance straight to RETIRED with its reason.
// It is a compare-and-swap on state: retired reports false if the instance
// was no longer VALID (another run already retired it).
func (r *TeamRepo) RetireInstance(ctx context.Context, instanceID string, reason model.RetireReason, runID string) (retired bool, err error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE instances SET state = 'RETIRED', retire_reason = $1, retired_at = now(), retired_by_run_id = $2
		FROM slots s WHERE instances.id = $3 AND s.id = instances.slot_id AND s.team_id = $4
		  AND instances.state = 'VALID'`, string(reason), runID, instanceID, r.teamID)
	return tag.RowsAffected() == 1, err
}

func (r *TeamRepo) expectOne(tag interface{ RowsAffected() int64 }, err error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// --- runs -------------------------------------------------------------------

// CreateRun queues a manual run for this team.
func (r *TeamRepo) CreateRun(ctx context.Context, mode RunMode, scope RunScope, requestedBy *string) (string, error) {
	scopeJSON, err := json.Marshal(scope)
	if err != nil {
		return "", err
	}
	var id string
	err = r.pool.QueryRow(ctx, `
		INSERT INTO scan_runs (team_id, trigger, mode, scope, requested_by)
		VALUES ($1, 'manual', $2, $3, $4) RETURNING id`,
		r.teamID, string(mode), scopeJSON, requestedBy,
	).Scan(&id)
	return id, err
}

const runColumns = `id, team_id, trigger, mode, scope, status, requested_by, scheduled_for, sync_error, error,
	total_count, valid_count, invalid_count, generated_count, error_count, created_at, started_at, finished_at`

func scanRun(row pgx.Row) (*Run, error) {
	var run Run
	var mode, status string
	var scopeJSON []byte
	err := row.Scan(&run.ID, &run.TeamID, &run.Trigger, &mode, &scopeJSON, &status, &run.RequestedBy,
		&run.ScheduledFor, &run.SyncError, &run.Error,
		&run.Counts.Total, &run.Counts.Valid, &run.Counts.Invalid, &run.Counts.Generated, &run.Counts.Errors,
		&run.CreatedAt, &run.StartedAt, &run.FinishedAt)
	if err != nil {
		return nil, err
	}
	run.Mode, run.Status = RunMode(mode), RunStatus(status)
	if err := json.Unmarshal(scopeJSON, &run.Scope); err != nil {
		return nil, fmt.Errorf("decoding run scope: %w", err)
	}
	return &run, nil
}

func (r *TeamRepo) Run(ctx context.Context, runID string) (*Run, error) {
	run, err := scanRun(r.pool.QueryRow(ctx,
		`SELECT `+runColumns+` FROM scan_runs WHERE id = $1 AND team_id = $2`, runID, r.teamID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return run, err
}

// FinishRun records a run's final status, counts and errors.
func (r *TeamRepo) FinishRun(ctx context.Context, runID string, status RunStatus, c Counts, syncErr, runErr *string) error {
	return r.expectOne(r.pool.Exec(ctx, `
		UPDATE scan_runs SET status = $1, total_count = $2, valid_count = $3, invalid_count = $4,
		       generated_count = $5, error_count = $6, sync_error = $7, error = $8, finished_at = now()
		WHERE id = $9 AND team_id = $10`,
		string(status), c.Total, c.Valid, c.Invalid, c.Generated, c.Errors, syncErr, runErr, runID, r.teamID))
}

// AddItem records one test case's outcome within a run. The run must belong
// to this team.
func (r *TeamRepo) AddItem(ctx context.Context, runID string, it RunItem) error {
	return r.expectOne(r.pool.Exec(ctx, `
		INSERT INTO scan_run_items (run_id, slot_id, test_case_key, environment, outcome, old_pnr, new_pnr, failed_rule, error)
		SELECT id, $2, $3, $4, $5, $6, $7, $8, $9 FROM scan_runs WHERE id = $1 AND team_id = $10`,
		runID, it.SlotID, it.TestCaseKey, it.Environment, string(it.Outcome),
		it.OldPNR, it.NewPNR, it.FailedRule, it.Error, r.teamID))
}

func (r *TeamRepo) Items(ctx context.Context, runID string) ([]RunItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT i.id, i.slot_id, i.test_case_key, i.environment, i.outcome, i.old_pnr, i.new_pnr,
		       i.failed_rule, i.error, i.created_at
		FROM scan_run_items i JOIN scan_runs r ON r.id = i.run_id
		WHERE i.run_id = $1 AND r.team_id = $2
		ORDER BY i.test_case_key, i.environment, i.created_at`, runID, r.teamID)
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
