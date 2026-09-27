package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	_ "time/tzdata" // team timezones must resolve even on hosts without zoneinfo

	"github.com/soumya-ranjan-000/tdms/internal/auth"
	"github.com/soumya-ranjan-000/tdms/internal/notify"
	"github.com/soumya-ranjan-000/tdms/internal/scheduler"
	"github.com/soumya-ranjan-000/tdms/internal/web"
	"github.com/soumya-ranjan-000/tdms/internal/worker"
)

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	defaultAddr := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		defaultAddr = ":" + port
	}
	addr := fs.String("addr", defaultAddr, "listen address")
	fs.Parse(args)

	store, pool, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	qmetryClient := qmetryFromEnv()
	if qmetryClient == nil {
		log.Println("warning: QMetry credentials not set; scans will skip sync and use stored blocks")
	}
	mailer := notify.FromEnv()
	if mailer == nil {
		log.Println("warning: SMTP_HOST not set; report emails are disabled")
	}

	w := worker.New(store, qmetryClient, mailer)
	sched := &scheduler.Scheduler{Store: store, Wake: w.Wake}
	ui, err := web.New(web.Config{
		Store: store, QMetry: qmetryClient, Wake: w.Wake,
		InsecureCookies: os.Getenv("TDMS_INSECURE_COOKIES") == "1",
	})
	if err != nil {
		return err
	}

	retention := logRetention()
	log.Printf("run logs are kept for %d days", int(retention.Hours()/24))

	var bg sync.WaitGroup
	bg.Go(func() { w.Run(ctx) })
	bg.Go(func() { sched.Run(ctx) })
	bg.Go(func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := store.PurgeExpiredSessions(ctx, auth.IdleTimeout); err != nil {
					log.Printf("purging sessions: %v", err)
				}
				if n, err := store.PurgeRunLogs(ctx, retention); err != nil {
					log.Printf("purging run logs: %v", err)
				} else if n > 0 {
					log.Printf("purged %d run log line(s) older than %d days", n, int(retention.Hours()/24))
				}
			}
		}
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           ui.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute, // "Test connection" waits on QMetry
		IdleTimeout:       2 * time.Minute,
	}
	serveErr := make(chan error, 1)
	go func() {
		log.Printf("TDMS listening on %s", *addr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Println("shutting down…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("http shutdown: %v", err)
		}
	}
	bg.Wait()
	return nil
}

// logRetention is how long run log lines are kept: TDMS_LOG_RETENTION_DAYS,
// default 30. Run summaries and per-test-case results are never pruned.
func logRetention() time.Duration {
	days := 30
	if v, err := strconv.Atoi(os.Getenv("TDMS_LOG_RETENTION_DAYS")); err == nil && v > 0 {
		days = v
	}
	return time.Duration(days) * 24 * time.Hour
}
