// Package worker executes queued scan runs — scheduled and manual alike —
// one at a time, and emails each run's report when it finishes.
package worker

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/notify"
	"github.com/soumya-ranjan-000/tdms/internal/qmetry"
	"github.com/soumya-ranjan-000/tdms/internal/runlog"
	"github.com/soumya-ranjan-000/tdms/internal/scan"
	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

// staleGeneration is how long an instance may sit in GENERATING before it
// is assumed abandoned by a crashed process.
const staleGeneration = 15 * time.Minute

type Worker struct {
	Store  *storage.Store
	QMetry *qmetry.Client // nil when QMetry credentials aren't configured
	Mailer *notify.Mailer // nil when email is disabled
	wake   chan struct{}
}

func New(store *storage.Store, qmetryClient *qmetry.Client, mailer *notify.Mailer) *Worker {
	return &Worker{Store: store, QMetry: qmetryClient, Mailer: mailer, wake: make(chan struct{}, 1)}
}

// Wake asks the worker to look for queued runs now rather than at its next
// poll. It never blocks.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run processes queued runs until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	if n, err := w.Store.FailInterruptedRuns(ctx); err != nil {
		log.Printf("worker: failing interrupted runs: %v", err)
	} else if n > 0 {
		log.Printf("worker: marked %d interrupted run(s) failed", n)
	}

	poll := time.NewTicker(10 * time.Second)
	defer poll.Stop()
	for {
		if _, _, err := w.Store.SweepStaleGenerating(ctx, staleGeneration); err != nil && ctx.Err() == nil {
			log.Printf("worker: sweeping stale generations: %v", err)
		}
		for w.runNext(ctx) {
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-poll.C:
		}
	}
}

// runNext executes one queued run, reporting whether it found one.
func (w *Worker) runNext(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	run, err := w.Store.NextQueuedRun(ctx)
	if err != nil {
		log.Printf("worker: finding queued run: %v", err)
		return false
	}
	if run == nil {
		return false
	}
	if err := w.Store.StartRun(ctx, run.ID); err != nil {
		if !errors.Is(err, storage.ErrRunInProgress) {
			log.Printf("worker: starting run %s: %v", run.ID, err)
		}
		return false
	}

	repo := w.Store.Team(run.TeamID)
	runLog := runlog.New(repo, run.ID, nil)
	defer runLog.Close()
	team, err := repo.Settings(ctx)
	if err != nil {
		msg := "loading team settings: " + err.Error()
		runLog.Error("", "", "Run failed before starting: %s", msg)
		repo.FinishRun(context.WithoutCancel(ctx), run.ID, storage.RunFailed, storage.Counts{}, nil, &msg)
		return true
	}

	log.Printf("worker: run %s (team %s, %s) started", run.ID, team.Name, run.Mode)
	runner := scan.New(repo, team, w.QMetry)
	runner.Log = runLog
	if err := runner.Execute(ctx, run); err != nil {
		log.Printf("worker: run %s failed: %v", run.ID, err)
	} else {
		log.Printf("worker: run %s finished: %+v", run.ID, run.Counts)
	}
	w.notify(context.WithoutCancel(ctx), repo, team, run, runLog)
	return true
}

func (w *Worker) notify(ctx context.Context, repo *storage.TeamRepo, team *storage.Team, run *storage.Run, runLog *runlog.Logger) {
	if w.Mailer == nil {
		runLog.Info("", "", "Report emails: not sent (email is not configured on the server)")
		return
	}
	recipients, err := repo.Recipients(ctx)
	if err != nil {
		runLog.Error("", "", "Report emails: could not load recipients: %v", err)
		return
	}
	if len(recipients) == 0 {
		runLog.Info("", "", "Report emails: none sent (no recipients configured)")
		return
	}
	items, err := repo.Items(ctx, run.ID)
	if err != nil {
		runLog.Error("", "", "Report emails: could not load the run's results: %v", err)
		return
	}
	emails := notify.Compose(team, run, items, recipients, w.Mailer.BaseURL)
	if len(emails) == 0 {
		runLog.Info("", "", "Report emails: none sent (nothing matched the recipients' subscriptions)")
	}
	for _, e := range emails {
		if err := w.Mailer.Send(e); err != nil {
			runLog.Error("", "", "Report email to %s failed: %v", e.To, err)
			continue
		}
		runLog.Info("", "", "Report emailed to %s", e.To)
	}
}
