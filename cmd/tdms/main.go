// Command tdms is TDMS's entrypoint:
//
//	tdms serve                                    web UI + worker + scheduler
//	tdms add-environment --name --pss-url         operator: register an environment
//	tdms create-team --name --project --env --admin-email
//	                                              operator: create a team and its first admin
//	tdms scan --team --mode check|heal [--env]    run one scan synchronously
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/soumya-ranjan-000/tdms/internal/auth"
	"github.com/soumya-ranjan-000/tdms/internal/qmetry"
	"github.com/soumya-ranjan-000/tdms/internal/runlog"
	"github.com/soumya-ranjan-000/tdms/internal/scan"
	"github.com/soumya-ranjan-000/tdms/internal/storage"
	"github.com/soumya-ranjan-000/tdms/migrations"
)

const usage = `usage: tdms <command> [flags]

commands:
  serve             run the web UI, worker and scheduler
  add-environment   register an environment and its PSS URL (operator)
  create-team       create a team with its QMetry project and first admin (operator)
  scan              run one scan for a team and print the report`

func main() {
	_ = godotenv.Load() // .env is optional; real env vars always win
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "serve":
		err = runServe(ctx, args)
	case "add-environment":
		err = runAddEnvironment(ctx, args)
	case "create-team":
		err = runCreateTeam(ctx, args)
	case "scan":
		err = runScan(ctx, args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s\n", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// openStore connects to DATABASE_URL and applies pending migrations.
func openStore(ctx context.Context) (*storage.Store, *pgxpool.Pool, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, nil, errors.New("DATABASE_URL is not set")
	}
	pool, err := storage.Connect(ctx, databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := storage.Migrate(ctx, pool, migrations.FS); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("migrating: %w", err)
	}
	return storage.NewStore(pool), pool, nil
}

// qmetryFromEnv returns the shared QMetry service-account client, or nil if
// its credentials aren't configured.
func qmetryFromEnv() *qmetry.Client {
	baseURL, email := os.Getenv("QMETRY_BASE_URL"), os.Getenv("JIRA_EMAIL")
	token, apiKey := os.Getenv("JIRA_API_TOKEN"), os.Getenv("QMETRY_API_KEY")
	if baseURL == "" || email == "" || token == "" || apiKey == "" {
		return nil
	}
	return qmetry.New(baseURL, email, token, apiKey)
}

func runAddEnvironment(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("add-environment", flag.ExitOnError)
	name := fs.String("name", "", "environment name, e.g. acp-dev")
	pssURL := fs.String("pss-url", "", "base URL of this environment's PSS")
	fs.Parse(args)
	if *name == "" || *pssURL == "" {
		return errors.New("--name and --pss-url are required")
	}

	store, pool, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.AddEnvironment(ctx, *name, *pssURL); err != nil {
		return err
	}
	fmt.Printf("environment %s -> %s\n", *name, *pssURL)
	return nil
}

func runCreateTeam(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("create-team", flag.ExitOnError)
	name := fs.String("name", "", "team name, e.g. booking-squad")
	project := fs.String("project", "", "QMetry project key the team owns, e.g. ACP (operator-assigned)")
	envs := fs.String("env", "", "comma-separated environments to enroll the team in")
	adminEmail := fs.String("admin-email", "", "email of the team's first admin")
	adminName := fs.String("admin-name", "", "display name of the first admin")
	field := fs.String("field", "", "optional initial TDMS custom field id (admins can change it)")
	folder := fs.String("folder", "", "optional initial test case folder path (admins can change it)")
	fs.Parse(args)
	if *name == "" || *project == "" || *envs == "" || *adminEmail == "" {
		return errors.New("--name, --project, --env and --admin-email are required")
	}

	store, pool, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	password, err := auth.TempPassword()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	teamID, err := store.CreateTeam(ctx, *name, *project, splitList(*envs),
		storage.User{Email: *adminEmail, Name: *adminName, PasswordHash: hash})
	if err != nil {
		return err
	}
	if *field != "" || *folder != "" {
		if err := store.Team(teamID).UpdateIntegration(ctx, *field, *folder); err != nil {
			return err
		}
	}

	fmt.Printf("team %s created (project %s)\n", *name, *project)
	fmt.Printf("admin login: %s\ntemporary password: %s\n", strings.ToLower(*adminEmail), password)
	fmt.Println("(the admin must change it at first login)")
	return nil
}

func runScan(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	teamName := fs.String("team", "", "team name")
	mode := fs.String("mode", "check", "check (report only) or heal (retire and regenerate invalid data)")
	env := fs.String("env", "", "optional: limit to one environment")
	fs.Parse(args)
	if *teamName == "" {
		return errors.New("--team is required")
	}
	runMode := storage.RunMode(*mode)
	if runMode != storage.ModeCheck && runMode != storage.ModeHeal {
		return errors.New("--mode must be check or heal")
	}

	store, pool, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	teamID, err := store.TeamIDByName(ctx, *teamName)
	if err != nil {
		return fmt.Errorf("team %q: %w", *teamName, err)
	}
	repo := store.Team(teamID)
	team, err := repo.Settings(ctx)
	if err != nil {
		return err
	}
	runID, err := repo.CreateRun(ctx, runMode, storage.RunScope{Environment: *env}, nil)
	if err != nil {
		return err
	}
	if err := store.StartRun(ctx, runID); err != nil {
		return err
	}
	run, err := repo.Run(ctx, runID)
	if err != nil {
		return err
	}

	runLog := runlog.New(repo, run.ID, os.Stdout)
	runner := scan.New(repo, team, qmetryFromEnv())
	runner.Log = runLog
	runErr := runner.Execute(ctx, run)
	runLog.Close()
	fmt.Println()
	printRun(ctx, repo, run)
	return runErr
}

func printRun(ctx context.Context, repo *storage.TeamRepo, run *storage.Run) {
	fmt.Printf("run %s  mode=%s  status=%s\n", run.ID, run.Mode, run.Status)
	if run.SyncError != nil {
		fmt.Printf("sync error: %s\n", *run.SyncError)
	}
	items, err := repo.Items(ctx, run.ID)
	if err != nil {
		fmt.Println("could not load items:", err)
		return
	}
	for _, it := range items {
		fmt.Printf("  %-10s %-8s %-17s old=%-7s new=%-7s rule=%s %s\n",
			it.TestCaseKey, it.Environment, it.Outcome,
			deref(it.OldPNR), deref(it.NewPNR), deref(it.FailedRule), deref(it.Error))
	}
	c := run.Counts
	fmt.Printf("total=%d valid=%d invalid=%d generated=%d errors=%d\n", c.Total, c.Valid, c.Invalid, c.Generated, c.Errors)
}

func deref(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
