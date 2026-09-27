// Command tdms is TDMS's CLI entrypoint. Today it has one subcommand,
// sync, which runs one ingest pass for a single test case end-to-end:
// fetch the block from QMetry, unwrap+parse it, validate every rule
// against the dictionary, and compute its change-detection hash. This is
// the ingest routine from "Sync and change detection", minus the
// database write and the live-PNR check, which land once storage and a
// PSS adapter are wired in.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"github.com/soumya-ranjan-000/tdms/internal/dictionary"
	"github.com/soumya-ranjan-000/tdms/internal/ingest"
	"github.com/soumya-ranjan-000/tdms/internal/qmetry"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load() // .env is optional; real env vars always win

	testCaseID := flag.String("testcase", "", "QMetry test case id, the public API's opaque id (e.g. 854KiLA8uKmAmv)")
	version := flag.Int("version", 1, "test case version number")
	fieldID := flag.String("field", "qcf_7932330", "TDMS custom field id (TDMS Requirement & Rules, project ACP)")
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

	cf, err := client.GetCustomField(*testCaseID, *version, *fieldID)
	if err != nil {
		return fmt.Errorf("fetching custom field: %w", err)
	}

	block, err := qmetry.ParseBlock(cf.Value)
	if err != nil {
		return fmt.Errorf("parsing block: %w", err)
	}

	dict := dictionary.Seed()
	fmt.Printf("dictionary: v%s\n\n", dict.Version)

	for _, rule := range block.Validity.Rules {
		checks, err := dict.Compile(rule)
		if err != nil {
			return fmt.Errorf("rule %q failed dictionary validation: %w", rule.Name, err)
		}
		for _, c := range checks {
			fmt.Printf("compiled check: field=%-20s operator=%-8s value=%v\n", c.Field, c.Operator, c.Value)
		}
	}

	hash, err := ingest.Hash(block)
	if err != nil {
		return fmt.Errorf("hashing block: %w", err)
	}

	fmt.Println()
	fmt.Printf("test case:   %s\n", *testCaseID)
	fmt.Printf("class:       %s\n", block.Validity.Class)
	fmt.Printf("requirement: %+v\n", block.Requirement)
	fmt.Printf("block hash:  %s\n", hash)
	return nil
}
