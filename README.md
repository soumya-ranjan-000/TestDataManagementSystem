# TDMS

Test Data Management System — implements the design in
`Test Data Management System (TDMS) — Requirements & Design.md`. Self-heals
airline test data: reads a QA-declared requirement + validity block off
each QMetry test case, keeps a live PNR matching it, and regenerates
whenever the live check finds it unusable.

## Layout

| Path | Design doc section | What it does |
| --- | --- | --- |
| `internal/model` | "Requirement vs rules", "Slots and instances" | `Block` (Requirement + Validity), `Slot`, `Instance`, lifecycle states |
| `internal/dictionary` | "Rule schema and dictionary" | Versioned Fields/Operators registries + the two rule templates (`booking_status`, `checkin_window`) that compile a YAML rule into a generic `(field, operator, value)` check, validated legal against the dictionary |
| `internal/rules` | "Rule schema and dictionary" (the evaluator) | One generic evaluator: runs a compiled check against a live PNR snapshot |
| `internal/qmetry` | "Storage and database", "The authoring block" | QTM4J Cloud API client + block parsing, including unwrapping the `{code:...}` Jira wiki-markup macro QMetry's UI adds on hand-edits |
| `internal/ingest` | "Sync and change detection" | Canonical hash of a block, so reformatting never looks like a real change |
| `internal/storage`, `migrations/` | "Storage and database" | Postgres schema (slots/instances) — **not wired into the CLI yet**, see Status |
| `cmd/tdms` | "Sync and change detection" (the ingest routine) | CLI: `sync`-style single-test-case ingest pass |

## Status

Working end-to-end for one slice: given a QMetry test case id, `cmd/tdms`
fetches its `TDMS Requirement & Rules` custom field, unwraps/parses the
YAML block, validates every rule against the dictionary, and prints its
change-detection hash. Verified live against project ACP's `ACP-TC-1`.

Not yet wired in (each is a real next step, not a design gap):
- **Persistence.** `internal/storage` + `migrations/0001_init.sql` define
  the schema but nothing writes to it yet — `cmd/tdms` only prints what it
  found.
- **The live PNR check.** `internal/rules` can evaluate a compiled check,
  but nothing yet calls the airline/PSS system to build a `PNRSnapshot` —
  see `pss_system/` in the sibling `RAG-Chatbot-Project` repo for what
  that adapter would talk to.
- **Generation.** Nothing creates a PNR yet (`EmptySlot -> Generating`).
- **The twice-daily scan.** No scheduler; `cmd/tdms sync` currently does
  one test case, once, on demand.
- **The AI authoring tool.** Explicitly deferred in the design doc itself.

## Running it

```bash
cp .env.example .env   # fill in QMETRY_* — see QMetry/README.md in RAG-Chatbot-Project
go run ./cmd/tdms --testcase <qmetry-test-case-id>
```

`--field` defaults to `qcf_7932330`, the `TDMS Requirement & Rules` custom
field id on project ACP — pass a different one for another project.

## Local dev setup

Go isn't installed system-wide on this machine; a local toolchain lives at
`~/.local/go` (not on `PATH` by default — see `go.mod`'s toolchain
directive, or run `export PATH="$HOME/.local/go/bin:$PATH"`).
