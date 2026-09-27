// Command tdms is TDMS's CLI entrypoint. Today it runs one ingest pass for
// a single test case end-to-end: fetch the block from QMetry, unwrap+parse
// it, validate every rule against the dictionary, compute its
// change-detection hash, and (if DATABASE_URL is set) upsert its slot. If
// --pnr is given, it also runs the live health check against that PNR —
// "the live check runs ... at the moment data is handed to a test."
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"github.com/soumya-ranjan-000/tdms/internal/dictionary"
	"github.com/soumya-ranjan-000/tdms/internal/ingest"
	"github.com/soumya-ranjan-000/tdms/internal/model"
	"github.com/soumya-ranjan-000/tdms/internal/pss"
	"github.com/soumya-ranjan-000/tdms/internal/qmetry"
	"github.com/soumya-ranjan-000/tdms/internal/rules"
	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load() // .env is optional; real env vars always win
	ctx := context.Background()

	testCaseID := flag.String("testcase", "", "QMetry test case id, the public API's opaque id (e.g. 854KiLA8uKmAmv)")
	version := flag.Int("version", 1, "test case version number")
	fieldID := flag.String("field", "qcf_7932330", "TDMS custom field id (TDMS Requirement & Rules, project ACP)")
	environment := flag.String("environment", "acp-dev", "environment label stored on the slot")
	pnr := flag.String("pnr", "", "optional: also run the live health check against this PNR")
	flag.Parse()

	if *testCaseID == "" {
		return fmt.Errorf("--testcase is required")
	}

	baseURL := os.Getenv("QMETRY_BASE_URL")
	email := os.Getenv("JIRA_EMAIL")
	token := os.Getenv("JIRA_API_TOKEN")
	apiKey := os.Getenv("QMETRY_API_KEY")
	if baseURL == "" || email == "" || token == "" || apiKey == "" {
		return fmt.Errorf("missing one of QMETRY_BASE_URL/JIRA_EMAIL/JIRA_API_TOKEN/QMETRY_API_KEY in env")
	}

	client := qmetry.New(baseURL, email, token, apiKey)

	tcVersion, err := client.GetTestCaseVersion(*testCaseID, *version, *fieldID)
	if err != nil {
		return fmt.Errorf("fetching custom field: %w", err)
	}

	block, err := qmetry.ParseBlock(tcVersion.CustomField.Value)
	if err != nil {
		return fmt.Errorf("parsing block: %w", err)
	}

	dict := dictionary.Seed()
	fmt.Printf("dictionary: v%s\n\n", dict.Version)

	var compiled []dictionary.CompiledCheck
	for _, rule := range block.Validity.Rules {
		checks, err := dict.Compile(rule)
		if err != nil {
			return fmt.Errorf("rule %q failed dictionary validation: %w", rule.Name, err)
		}
		compiled = append(compiled, checks...)
		for _, c := range checks {
			fmt.Printf("compiled check: field=%-20s operator=%-8s value=%v\n", c.Field, c.Operator, c.Value)
		}
	}

	hash, err := ingest.Hash(block)
	if err != nil {
		return fmt.Errorf("hashing block: %w", err)
	}

	fmt.Println()
	fmt.Printf("test case:   %s (%s)\n", tcVersion.Key, *testCaseID)
	fmt.Printf("class:       %s\n", block.Validity.Class)
	fmt.Printf("requirement: %+v\n", block.Requirement)
	fmt.Printf("block hash:  %s\n", hash)

	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		if err := persist(ctx, databaseURL, tcVersion.Key, *testCaseID, *environment, block, hash); err != nil {
			return fmt.Errorf("persisting slot: %w", err)
		}
	} else {
		fmt.Println("\n(DATABASE_URL not set — skipping persistence)")
	}

	if *pnr != "" {
		if err := checkLivePNR(*pnr, compiled); err != nil {
			return fmt.Errorf("live PNR check: %w", err)
		}
	}

	return nil
}

func persist(ctx context.Context, databaseURL, testCaseKey, qmetryTCID, environment string, block *model.Block, hash string) error {
	pool, err := storage.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()

	if err := storage.Migrate(ctx, pool, "migrations"); err != nil {
		return fmt.Errorf("migrating: %w", err)
	}

	repo := storage.NewRepo(pool)
	changed, err := repo.UpsertSlot(ctx, testCaseKey, qmetryTCID, block.Validity.Class, environment, block, hash)
	if err != nil {
		return fmt.Errorf("upserting slot: %w", err)
	}
	if changed {
		fmt.Println("slot: created/updated (block changed)")
	} else {
		fmt.Println("slot: unchanged (hash matches what's stored — no-op)")
	}
	return nil
}

func checkLivePNR(pnr string, compiled []dictionary.CompiledCheck) error {
	baseURL := os.Getenv("PSS_BASE_URL")
	if baseURL == "" {
		baseURL = "https://rag-chatbot-project-1.onrender.com"
	}
	client := pss.New(baseURL)

	booking, err := client.GetBooking(pnr)
	if err != nil {
		return fmt.Errorf("fetching live booking: %w", err)
	}
	snap := pss.Snapshot(booking)

	fmt.Printf("\nlive PNR %s: raw status=%q -> canonical=%v\n", pnr, booking.Status, snap["booking.status"])

	allValid := true
	for _, check := range compiled {
		ok, err := rules.Evaluate(check, snap)
		status := "PASS"
		if err != nil {
			status = "ERROR: " + err.Error()
			allValid = false
		} else if !ok {
			status = "FAIL"
			allValid = false
		}
		fmt.Printf("  rule check: field=%-20s operator=%-8s -> %s\n", check.Field, check.Operator, status)
	}

	if allValid {
		fmt.Println("verdict: VALID")
	} else {
		fmt.Println("verdict: NOT VALID (would be retired and regenerated)")
	}
	return nil
}
