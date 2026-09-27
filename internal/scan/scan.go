// Package scan is the self-healing loop: one run syncs a team's blocks from
// its test management app into the database, then evaluates each slot's
// live PNR against the stored block and — depending on the run's mode —
// reports, retires, and regenerates. The CLI, the worker and the web UI all
// run scans through here, and every run writes a step-by-step activity log.
package scan

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/dictionary"
	"github.com/soumya-ranjan-000/tdms/internal/generate"
	"github.com/soumya-ranjan-000/tdms/internal/ingest"
	"github.com/soumya-ranjan-000/tdms/internal/model"
	"github.com/soumya-ranjan-000/tdms/internal/pss"
	"github.com/soumya-ranjan-000/tdms/internal/qmetry"
	"github.com/soumya-ranjan-000/tdms/internal/runlog"
	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

type Runner struct {
	Repo *storage.TeamRepo
	// Source is where the team's test cases live. When nil (not configured
	// or unbuildable), sync is skipped with SourceErr recorded and the run
	// evaluates the blocks already stored.
	Source    Source
	SourceErr error
	Dict      *dictionary.Dictionary
	NewPSS    func(baseURL string) *pss.Client
	// Log receives the run's activity log. When nil, Execute opens one for
	// the run and closes it when done; a caller that wants to add lines
	// after the run (the worker, for emails) passes its own and closes it.
	Log *runlog.Logger
}

// New builds a Runner for one team from its settings. qmetryClient may be
// nil when QMetry credentials aren't configured; runs then skip sync and
// evaluate the blocks already stored.
func New(repo *storage.TeamRepo, team *storage.Team, qmetryClient *qmetry.Client) *Runner {
	r := &Runner{Repo: repo, Dict: dictionary.Seed(), NewPSS: pss.New}
	if qmetryClient == nil {
		r.SourceErr = errors.New("QMetry credentials are not configured on the server")
		return r
	}
	r.Source, r.SourceErr = NewSource(team, qmetryClient)
	return r
}

// Execute performs an already-started run to completion, recording one item
// per test case and finishing the run. A returned error means the run
// itself failed (it is also recorded on the run); per-test-case problems
// are items, not run failures.
func (r *Runner) Execute(ctx context.Context, run *storage.Run) error {
	if r.Log == nil {
		r.Log = runlog.New(r.Repo, run.ID, nil)
		defer func() { r.Log.Close(); r.Log = nil }()
	}
	started := time.Now()
	r.Log.Info("", "", "Run started: mode %s, %s, scope %s", run.Mode, run.Trigger, describeScope(run.Scope))

	var counts storage.Counts
	record := func(it storage.RunItem) error {
		tally(&counts, it.Outcome)
		return r.Repo.AddItem(ctx, run.ID, it)
	}

	err := r.execute(ctx, run, record)
	status := storage.RunSucceeded
	var runErr *string
	if err != nil {
		status = storage.RunFailed
		runErr = strPtr(err.Error())
		r.Log.Error("", "", "Run failed: %v", err)
	}
	if ferr := r.Repo.FinishRun(context.WithoutCancel(ctx), run.ID, status, counts, run.SyncError, runErr); ferr != nil {
		r.Log.Error("", "", "Could not record the run's result: %v", ferr)
		return errors.Join(err, fmt.Errorf("finishing run: %w", ferr))
	}
	run.Status, run.Counts, run.Error = status, counts, runErr
	r.Log.Info("", "", "Run finished: %s — %d checked, %d valid, %d invalid, %d generated, %d errors (took %s)",
		status, counts.Total, counts.Valid, counts.Invalid, counts.Generated, counts.Errors, since(started))
	return err
}

func describeScope(s storage.RunScope) string {
	switch {
	case len(s.SlotIDs) > 0:
		return fmt.Sprintf("%d selected test case(s)", len(s.SlotIDs))
	case s.Environment != "":
		return "environment " + s.Environment
	default:
		return "whole folder, all environments"
	}
}

func since(t time.Time) string {
	d := time.Since(t)
	if d < time.Second {
		return strconv.FormatInt(d.Milliseconds(), 10) + " ms"
	}
	return d.Round(100 * time.Millisecond).String()
}

func (r *Runner) execute(ctx context.Context, run *storage.Run, record func(storage.RunItem) error) error {
	envs, err := r.Repo.Environments(ctx)
	if err != nil {
		return fmt.Errorf("loading environments: %w", err)
	}
	if len(envs) == 0 {
		return fmt.Errorf("team is not enrolled in any environment")
	}
	byName := map[string]storage.Environment{}
	var envNames []string
	for _, e := range envs {
		byName[e.Name] = e
		envNames = append(envNames, e.Name+" → "+e.PSSBaseURL)
	}
	r.Log.Info("", "", "Environments: %s", strings.Join(envNames, ", "))
	scopeEnvs := envs
	if run.Scope.Environment != "" {
		e, ok := byName[run.Scope.Environment]
		if !ok {
			return fmt.Errorf("team is not enrolled in environment %q", run.Scope.Environment)
		}
		scopeEnvs = []storage.Environment{e}
	}

	// A run over selected slots re-checks their stored blocks; only a
	// folder- or environment-wide run syncs from the test management app.
	if len(run.Scope.SlotIDs) == 0 {
		if err := r.sync(ctx, run, scopeEnvs, record); err != nil {
			run.SyncError = strPtr(err.Error())
			r.Log.Error("", "", "Sync failed, so the blocks already stored will be used: %v", err)
		}
	} else {
		r.Log.Info("", "", "Sync skipped: selected test cases are checked against their stored blocks")
	}

	slots, err := r.Repo.SlotsInScope(ctx, run.Scope)
	if err != nil {
		return fmt.Errorf("loading slots: %w", err)
	}
	r.Log.Info("", "", "Evaluating %d slot(s)", len(slots))
	for _, slot := range slots {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := r.evaluate(ctx, run, slot, byName[slot.Environment])
		if err := record(item); err != nil {
			return fmt.Errorf("recording %s: %w", slot.TestCaseKey, err)
		}
		if err := r.Repo.TouchSlot(ctx, slot.ID, item.Outcome); err != nil {
			return fmt.Errorf("updating %s: %w", slot.TestCaseKey, err)
		}
	}
	return nil
}

// sync copies the team's current blocks into the database — the design
// doc's ingest step. It never calls PSS. A failure here is recorded on the
// run but doesn't stop evaluation of the blocks already stored.
func (r *Runner) sync(ctx context.Context, run *storage.Run, envs []storage.Environment, record func(storage.RunItem) error) error {
	if r.Source == nil {
		if r.SourceErr != nil {
			return fmt.Errorf("sync skipped: %w", r.SourceErr)
		}
		return fmt.Errorf("sync skipped: no test management app configured")
	}
	started := time.Now()
	r.Log.Info("", "", "Sync: listing test cases from the test management app")
	listing, err := r.Source.List(ctx)
	if err != nil {
		return fmt.Errorf("listing test cases: %w", err)
	}
	withBlock := 0
	for _, tc := range listing.Cases {
		if strings.TrimSpace(tc.RawBlock) != "" {
			withBlock++
		}
	}
	r.Log.Info("", "", "Sync: found %d test case(s) in %d folder(s); %d carry a TDMS block (took %s)",
		len(listing.Cases), len(listing.Folders), withBlock, since(started))
	if err := r.mirror(ctx, run, listing); err != nil {
		return err
	}

	var present []string
	changed := 0
	for _, tc := range listing.Cases {
		if strings.TrimSpace(tc.RawBlock) == "" {
			continue // this test case doesn't ask TDMS for data
		}
		present = append(present, tc.Key)

		block, err := qmetry.ParseBlock(tc.RawBlock)
		if err == nil {
			_, err = compile(r.Dict, *block)
		}
		if err != nil {
			r.Log.Error(tc.Key, "", "TDMS block is invalid, keeping the last good one: %v", err)
			for _, env := range envs {
				slotID, found, merr := r.Repo.MarkBlockError(ctx, tc.Key, env.Name, err.Error())
				if merr != nil {
					return merr
				}
				it := storage.RunItem{TestCaseKey: tc.Key, Environment: env.Name,
					Outcome: storage.OutcomeBlockError, Error: strPtr(err.Error())}
				if found {
					it.SlotID = &slotID
				}
				if err := record(it); err != nil {
					return err
				}
			}
			continue
		}

		hash, err := ingest.Hash(block)
		if err != nil {
			return fmt.Errorf("hashing %s: %w", tc.Key, err)
		}
		for _, env := range envs {
			_, blockChanged, err := r.Repo.UpsertSlot(ctx, storage.SlotUpsert{
				TestCaseKey: tc.Key, TCMTestCaseID: tc.ID, TCMVersion: tc.Version,
				Class: block.Validity.Class, Environment: env.Name, Block: block, BlockHash: hash,
			})
			if err != nil {
				return fmt.Errorf("storing %s/%s: %w", tc.Key, env.Name, err)
			}
			if blockChanged {
				changed++
				r.Log.Info(tc.Key, env.Name, "TDMS block is new or changed (version %d); stored", tc.Version)
			}
		}
	}
	r.Log.Info("", "", "Sync: %d block(s) new or changed, %d unchanged", changed, len(present)*len(envs)-changed)

	if shouldOrphan(run.Scope, present) {
		orphaned, err := r.Repo.OrphanMissing(ctx, present)
		if err != nil {
			return fmt.Errorf("orphaning: %w", err)
		}
		for _, o := range orphaned {
			r.Log.Warn(o.TestCaseKey, o.Environment, "Orphaned: no longer in the folder or lost its TDMS block; TDMS stops managing it")
			id := o.ID
			if err := record(storage.RunItem{SlotID: &id, TestCaseKey: o.TestCaseKey, Environment: o.Environment,
				Outcome: storage.OutcomeOrphaned}); err != nil {
				return err
			}
		}
	} else if len(run.Scope.SlotIDs) == 0 && run.Scope.IsFull() {
		r.Log.Warn("", "", "Orphan check skipped: the folder listed no test case with a TDMS block (check the folder path)")
	}
	return nil
}

// mirror stores the folder tree and every listed test case — with or
// without a TDMS block — so the UI can browse and search them without
// calling the test management app.
func (r *Runner) mirror(ctx context.Context, run *storage.Run, listing *Listing) error {
	if err := r.Repo.ReplaceFolders(ctx, listing.Folders); err != nil {
		return fmt.Errorf("storing folders: %w", err)
	}
	cases := make([]storage.MirrorCase, len(listing.Cases))
	keys := make([]string, len(listing.Cases))
	for i, tc := range listing.Cases {
		folderIDs := make([]int64, len(tc.FolderIDs))
		for j, id := range tc.FolderIDs {
			folderIDs[j] = int64(id)
		}
		cases[i] = storage.MirrorCase{TCMTestCaseID: tc.ID, Key: tc.Key, KeyNum: keyNumber(tc.Key),
			Summary: tc.Summary, Version: tc.Version, FolderIDs: folderIDs, RawBlock: qmetry.UnwrapCodeMacro(tc.RawBlock)}
		keys[i] = tc.Key
	}
	if err := r.Repo.MirrorTestCases(ctx, cases); err != nil {
		return err
	}
	if shouldOrphan(run.Scope, keys) {
		removed, err := r.Repo.MarkRemovedTestCases(ctx, keys)
		if err != nil {
			return fmt.Errorf("marking removed test cases: %w", err)
		}
		if removed > 0 {
			r.Log.Warn("", "", "Sync: %d test case(s) left the folder since the last sync", removed)
		}
	}
	return nil
}

// keyNumber is a key's trailing number (ACP-TC-12 → 12), so the table can
// sort ACP-TC-2 before ACP-TC-10.
func keyNumber(key string) int {
	end := len(key)
	start := end
	for start > 0 && key[start-1] >= '0' && key[start-1] <= '9' {
		start--
	}
	n, _ := strconv.Atoi(key[start:end])
	return n
}

// shouldOrphan guards the one destructive thing sync does. Only a complete
// listing of the whole configured folder can prove a test case is gone; a
// narrowed scope never lists everything, and an empty result is far more
// likely a folder-path typo than a team deleting every test case at once.
func shouldOrphan(scope storage.RunScope, present []string) bool {
	return scope.IsFull() && len(present) > 0
}

// evaluate checks one slot and, per the run's mode, heals it.
func (r *Runner) evaluate(ctx context.Context, run *storage.Run, slot model.Slot, env storage.Environment) storage.RunItem {
	key, envName := slot.TestCaseKey, slot.Environment
	item := storage.RunItem{SlotID: &slot.ID, TestCaseKey: key, Environment: envName}
	fail := func(outcome storage.Outcome, err error) storage.RunItem {
		r.Log.Error(key, envName, "%s: %v", outcome, err)
		item.Outcome, item.Error = outcome, strPtr(err.Error())
		return item
	}

	inst, err := r.Repo.CurrentInstance(ctx, slot.ID)
	if err != nil {
		return fail(storage.OutcomeCheckError, err)
	}

	if inst == nil {
		if run.Mode == storage.ModeCheck {
			r.Log.Warn(key, envName, "Slot is empty: no PNR yet (report only, nothing generated)")
			item.Outcome, item.FailedRule = storage.OutcomeInvalid, strPtr("empty_slot")
			return item
		}
		r.Log.Info(key, envName, "Slot is empty: generating a PNR")
		pnr, err := r.generate(ctx, run, slot, env)
		if pnr != "" {
			item.NewPNR = &pnr
		}
		if err != nil {
			return fail(storage.OutcomeGenerationFailed, err)
		}
		item.Outcome = storage.OutcomeGenerated
		return item
	}
	if inst.State == model.StateGenerating {
		return fail(storage.OutcomeCheckError, errors.New("a generation for this slot is still in progress"))
	}
	item.OldPNR = strPtr(inst.PNR)

	var reason model.RetireReason
	switch {
	case run.Mode == storage.ModeRegenerate:
		reason = model.ReasonManual
		r.Log.Info(key, envName, "Regenerate requested for PNR %s", inst.PNR)
	case inst.BlockHash != "" && inst.BlockHash != slot.BlockHash:
		reason = model.ReasonRequirementChanged
		item.FailedRule = strPtr("requirement_changed")
		r.Log.Warn(key, envName, "PNR %s was generated for an older version of the TDMS block (requirement changed)", inst.PNR)
	default:
		checks, err := compile(r.Dict, slot.Block)
		if err != nil {
			return fail(storage.OutcomeCheckError, err)
		}
		lookupStart := time.Now()
		booking, lookupErr := r.NewPSS(env.PSSBaseURL).GetBooking(ctx, inst.PNR)
		took := since(lookupStart)
		v := judge(booking, lookupErr, checks)
		switch v.kind {
		case verdictValid:
			r.Log.Info(key, envName, "PNR %s is valid (PSS %s): %s", inst.PNR, took, strings.Join(v.checks, "; "))
			r.refreshTestData(ctx, key, envName, inst, booking)
			item.Outcome = storage.OutcomeValid
			return item
		case verdictUnknown:
			return fail(storage.OutcomeCheckError, fmt.Errorf("could not check PNR %s (PSS %s), left in place: %w", inst.PNR, took, v.err))
		}
		reason = v.reason
		item.FailedRule = strPtr(v.failedRule)
		if v.failedRule == "pnr_not_found" {
			r.Log.Warn(key, envName, "PNR %s is invalid: PSS no longer has it (404, took %s) → %s", inst.PNR, took, reason)
		} else {
			r.Log.Warn(key, envName, "PNR %s is invalid (PSS %s): %s → %s", inst.PNR, took, strings.Join(v.checks, "; "), reason)
		}
	}

	if run.Mode == storage.ModeCheck {
		r.Log.Info(key, envName, "Report only: PNR %s left in place", inst.PNR)
		item.Outcome = storage.OutcomeInvalid
		return item
	}

	retired, err := r.Repo.RetireInstance(ctx, inst.ID, reason, run.ID)
	if err != nil {
		return fail(storage.OutcomeCheckError, fmt.Errorf("retiring %s: %w", inst.PNR, err))
	}
	if !retired {
		return fail(storage.OutcomeCheckError, errors.New("instance changed during the check; left for the next run"))
	}
	r.Log.Info(key, envName, "Retired PNR %s (reason: %s); generating a replacement", inst.PNR, reason)
	pnr, err := r.generate(ctx, run, slot, env)
	if pnr != "" {
		item.NewPNR = &pnr
	}
	if err != nil {
		return fail(storage.OutcomeGenerationFailed, err)
	}
	item.Outcome = storage.OutcomeRegenerated
	return item
}

// refreshTestData rewrites a live instance's test data from the booking PSS
// just returned — the source of truth for the passenger's name — but only
// when it differs, so a routine check costs no write. This also fills in
// test data for PNRs generated before it was recorded.
func (r *Runner) refreshTestData(ctx context.Context, key, envName string, inst *model.Instance, booking *pss.Booking) {
	lastName := booking.LeadLastName()
	if lastName == "" {
		return
	}
	want := []model.TestDatum{{PNR: inst.PNR, LastName: lastName}}
	if len(inst.TestData) == 1 && inst.TestData[0] == want[0] {
		return
	}
	if err := r.Repo.UpdateTestData(ctx, inst.ID, want); err != nil {
		r.Log.Warn(key, envName, "Could not update test data for %s: %v", inst.PNR, err)
		return
	}
	r.Log.Info(key, envName, "Test data for %s updated from PSS: last name %s", inst.PNR, lastName)
}

// generate runs "EmptySlot -> Generating -> Valid" for one slot. The PNR is
// recorded the moment PSS books it, so a failed payment or ticketing step
// leaves it in the trail rather than losing it.
func (r *Runner) generate(ctx context.Context, run *storage.Run, slot model.Slot, env storage.Environment) (string, error) {
	key, envName := slot.TestCaseKey, slot.Environment
	instanceID, err := r.Repo.BeginGeneration(ctx, slot.ID, run.ID, slot.BlockHash)
	if err != nil {
		return "", err
	}
	started := time.Now()
	gen := generate.New(r.NewPSS(env.PSSBaseURL))
	gen.Logf = func(format string, args ...any) { r.Log.Info(key, envName, format, args...) }
	booked, genErr := gen.Generate(ctx, slot.Block.Requirement, slot.TestCaseKey, func(d model.TestDatum) error {
		return r.Repo.RecordBookedPNR(ctx, instanceID, d.PNR, []model.TestDatum{d})
	})
	pnr := booked.PNR
	if genErr != nil {
		if err := r.Repo.FailGeneration(context.WithoutCancel(ctx), instanceID, pnr); err != nil {
			r.Log.Error(key, envName, "Could not record the failed generation: %v", err)
		} else if pnr != "" {
			r.Log.Warn(key, envName, "Booked PNR %s kept in the history as generation_failed", pnr)
		}
		return pnr, genErr
	}
	if err := r.Repo.CompleteGeneration(ctx, instanceID); err != nil {
		return pnr, fmt.Errorf("completing generation of %s: %w", pnr, err)
	}
	r.Log.Info(key, envName, "Generated PNR %s, last name %s (took %s)", pnr, booked.LastName, since(started))
	return pnr, nil
}

func tally(c *storage.Counts, o storage.Outcome) {
	c.Total++
	switch o {
	case storage.OutcomeValid:
		c.Valid++
	case storage.OutcomeInvalid:
		c.Invalid++
	case storage.OutcomeGenerated:
		c.Generated++
	case storage.OutcomeRegenerated:
		c.Invalid++
		c.Generated++
	case storage.OutcomeGenerationFailed, storage.OutcomeCheckError, storage.OutcomeBlockError:
		c.Errors++
	}
}

func strPtr(s string) *string { return &s }
