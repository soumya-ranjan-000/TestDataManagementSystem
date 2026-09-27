# TDMS

Test Data Management System — implements the design in
`Test Data Management System (TDMS) — Requirements & Design.md`. It
self-heals airline test data: it reads the requirement + validity block QA
write on each QMetry test case, keeps a live PNR matching it, and retires and
regenerates the PNR whenever the live check finds it unusable. Many teams
share one TDMS through a web UI, each seeing only its own data.

## How a run works

Every run — scheduled, a custom scan, or an action on selected test cases —
has two steps:

1. **Sync** — list the team's test cases in its QMetry folder (one search
   call returns every TDMS block) and store each block per environment. Test
   cases without a block are ignored; a test case that leaves the folder is
   *orphaned* (only after a complete, non-empty listing of the whole folder).
   A sync failure is recorded but doesn't stop step 2.
2. **Evaluate** — check each slot's live PNR against its **stored** block,
   then act by mode:
   - `check` — report only.
   - `heal` — retire invalid PNRs and generate fresh ones; fill empty slots.
   - `regenerate` — replace the PNRs of selected test cases even if valid.

A PNR is retired only on a positive finding, with a reason kept in its trail:

| Finding | Retire reason |
| --- | --- |
| `checkin_window` closed (now past departure − `closes_hrs`) | `expired` |
| `booking_status` failed and equals the rule's `consumed_when` | `consumed` |
| `booking_status` failed otherwise, or PSS returns 404 | `dead` |
| The test case's block changed since generation | `requirement_changed` |
| Regenerated from the UI | `manual` |

If PSS can't be reached or the data can't be evaluated, the outcome is
`check_error` and the PNR is left alone.

## Multi-team model

- **Isolation key:** `team_id`. Every table carries it or reaches it
  through `slots`; the web layer only ever gets a repo bound to the signed-in
  user's team, and another team's IDs answer 404.
- **Operator** (whoever runs the server) creates teams, assigns each team's
  QMetry project, and defines environments and their PSS URLs — from the CLI.
- **Team admins** configure the folder path, custom field, environments
  (from the operator's list), daily run times + timezone, report email
  recipients, and members.
- **Members** run scans, validate / heal / regenerate selected test cases,
  and read reports.
- One shared QMetry service account; its credentials stay in `.env`.

## Running it

```bash
cp .env.example .env    # QMETRY_*, DATABASE_URL, optional SMTP_* — see comments inside
go build -o tdms ./cmd/tdms

# Operator setup (once):
./tdms add-environment --name acp-dev --pss-url https://rag-chatbot-project-1.onrender.com
./tdms create-team --name booking-squad --project ACP --env acp-dev \
    --admin-email you@example.com --field qcf_7932330 --folder /chatbot-booking-retrival
#   → prints the admin's temporary password (changed at first sign-in)

./tdms serve            # UI on :8080, plus the worker and scheduler
```

Migrations apply automatically on start, into a dedicated `tdms` Postgres
schema. `DATABASE_URL` is either the local `docker compose up -d` Postgres or
a Supabase **Session pooler** URI (not the REST `SUPABASE_URL` the PSS uses;
the `tdms` schema keeps TDMS off Supabase's public Data API).

`./tdms scan --team booking-squad --mode check|heal` runs one scan
synchronously and prints the report — handy for testing without the UI.

## UI

| Page | For |
| --- | --- |
| Dashboard | Slot counts per environment, next scheduled run, recent runs and problems |
| Test cases | Every test case in the folder (mirrored from QMetry, with or without a TDMS block): folder tree, search by key or summary, filters, 5/10/20/50/100 rows per page (remembered), test data (PNR + last name) per environment. Expand a row for its raw YAML block and PNR history. Select rows → Validate / Heal / Regenerate |
| Custom scan | Folder- or environment-wide scan, report-only or heal |
| Runs | History, and a live-updating report per run with its **activity log**: every sync step, PSS check with rule-by-rule results, retirement and generation step. Filter by level, search, page, and download as `.txt`. Logs are kept `TDMS_LOG_RETENTION_DAYS` (default 30) |
| Creation report | Every PNR created in a date range, and what became of it |
| Settings (admin) | Integration + Test connection, environments, run times, report emails |
| Members (admin) | Add, reset, change role, remove |

Security: bcrypt passwords with forced change of temporary ones, hashed
server-side session tokens (12h absolute / 2h idle, HttpOnly + Secure +
SameSite=Lax cookies), per-IP and per-email login rate limiting, Go 1.25's
cross-origin protection against CSRF, and a strict Content-Security-Policy.

## Layout

| Path | What it does |
| --- | --- |
| `cmd/tdms` | CLI: `serve`, `add-environment`, `create-team`, `scan` |
| `internal/web` | Handlers, embedded templates, vendored htmx |
| `internal/auth` | Password hashing, temp passwords, session tokens |
| `internal/worker` | Runs queued runs one at a time; emails reports |
| `internal/scheduler` | Queues each team's daily heal runs in its timezone |
| `internal/scan` | The sync + evaluate pipeline, retire reasons, the `Source` interface for test management apps |
| `internal/notify` | SMTP report emails, one per recipient per run |
| `internal/storage`, `migrations/` | Postgres: global `Store` (operator, worker, login) and team-scoped `TeamRepo` |
| `internal/qmetry` | QMetry client: project/folder resolution, test-case search, block parsing |
| `internal/pss` | PSS client: booking lookup (404 → `ErrNotFound`) and generation calls |
| `internal/generate` | Requirement → live PNR |
| `internal/dictionary`, `internal/rules` | Rule vocabulary and the generic evaluator |
| `internal/ingest`, `internal/reldate`, `internal/model` | Block hashing, `T+90d` dates, shared types |

## Not done yet

- **Ancillaries.** Generation fails loudly if a requirement declares a
  seat/baggage/lounge other than `none` — that mapping isn't designed yet.
- **Reservation** (handing a PNR to a test run under a lock) — the schema has
  the columns; nothing uses them yet.
- **SHARED data across tests.** Each test case still gets its own slot; the
  design doc's many-tests-share-one-PNR dedup isn't built.
- **The AI authoring tool.** Deferred in the design doc itself.

**Block schema notes** (not yet reflected in the design doc):
- `requirement.return` (relative date, like `depart`) is required when
  `trip_type: round_trip`.
- `booking.status` accepts PSS's full status set (`HELD`, `CONFIRMED`,
  `TICKETED`, `CHECKED_IN`, `BOARDED`, `FLOWN`, `CANCELLED`, `REFUNDED`).
  Cancelling a *ticketed* booking in PSS yields `REFUNDED`, so a cancel test
  on a ticketed PNR should declare `consumed_when: REFUNDED`.

## Local dev setup

Go isn't installed system-wide on this machine; a local toolchain lives at
`~/.local/go` (run `export PATH="$HOME/.local/go/bin:$PATH"`).
`go test ./...` runs the unit tests (no database needed).
