-- Multi-team TDMS: teams, their users and settings, operator-managed
-- environments, and scan runs. Isolation is team_id: every table either
-- carries it or reaches it by joining through slots.

CREATE TABLE teams (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                TEXT NOT NULL UNIQUE,
    tcm_provider        TEXT NOT NULL DEFAULT 'qmetry' CHECK (tcm_provider IN ('qmetry')),
    tcm_project_key     TEXT NOT NULL,             -- operator-assigned; team admins cannot change it
    tcm_custom_field_id TEXT NOT NULL DEFAULT '',  -- the field holding the TDMS block
    tcm_folder_path     TEXT NOT NULL DEFAULT '',  -- e.g. 'chatbot-booking-retrival'; '' = whole project
    run_times           TEXT[] NOT NULL DEFAULT '{}', -- 'HH:MM' in the team's timezone
    timezone            TEXT NOT NULL DEFAULT 'UTC',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Operator-managed via the CLI, so a team admin can never point TDMS at an
-- arbitrary URL; admins only choose among these.
CREATE TABLE environments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL UNIQUE,
    pss_base_url    TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE team_environments (
    team_id         UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    environment_id  UUID NOT NULL REFERENCES environments(id),
    PRIMARY KEY (team_id, environment_id)
);

CREATE TABLE users (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id              UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    email                TEXT NOT NULL UNIQUE,     -- stored lowercased
    name                 TEXT NOT NULL DEFAULT '',
    password_hash        TEXT NOT NULL,
    role                 TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    must_change_password BOOLEAN NOT NULL DEFAULT true,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX users_team_id_idx ON users (team_id);

-- Only the SHA-256 of the session token is stored, so a leaked sessions
-- table can't be replayed as cookies.
CREATE TABLE sessions (
    token_hash      BYTEA PRIMARY KEY,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

CREATE TABLE team_recipients (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id         UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    email           TEXT NOT NULL,
    on_scan         BOOLEAN NOT NULL DEFAULT true,
    on_generation   BOOLEAN NOT NULL DEFAULT true,
    on_error        BOOLEAN NOT NULL DEFAULT true,
    UNIQUE (team_id, email)
);

CREATE TABLE scan_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id         UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    trigger         TEXT NOT NULL CHECK (trigger IN ('scheduled', 'manual')),
    mode            TEXT NOT NULL CHECK (mode IN ('check', 'heal', 'regenerate')),
    scope           JSONB NOT NULL DEFAULT '{}',   -- {} = whole folder; {"slot_ids":[...]} or {"environment":"..."}
    status          TEXT NOT NULL DEFAULT 'queued'
                        CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    requested_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    scheduled_for   TIMESTAMPTZ,                   -- set only for scheduled runs; the scheduler's claim key
    sync_error      TEXT,                          -- QMetry sync failed; evaluation still ran on stored blocks
    error           TEXT,                          -- the run itself failed
    total_count     INT NOT NULL DEFAULT 0,
    valid_count     INT NOT NULL DEFAULT 0,
    invalid_count   INT NOT NULL DEFAULT 0,
    generated_count INT NOT NULL DEFAULT 0,
    error_count     INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    UNIQUE (team_id, scheduled_for)                -- NULLs are distinct, so manual runs never collide
);
-- At most one running run per team: two runs regenerating the same slots
-- concurrently would race.
CREATE UNIQUE INDEX one_running_run_per_team ON scan_runs (team_id) WHERE status = 'running';
CREATE INDEX scan_runs_queued_idx ON scan_runs (created_at) WHERE status = 'queued';
CREATE INDEX scan_runs_team_idx ON scan_runs (team_id, created_at DESC);

-- Slots: scoped to a team and an operator-defined environment. A slot's
-- identity is (team_id, test_case_key, environment).
ALTER TABLE slots RENAME COLUMN qmetry_tc_id TO tcm_test_case_id;
ALTER TABLE slots ADD COLUMN team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE;
ALTER TABLE slots ADD COLUMN tcm_version INT NOT NULL DEFAULT 1;
ALTER TABLE slots ADD COLUMN block_error TEXT;          -- latest block failed to parse; `block` keeps the last good one
ALTER TABLE slots ADD COLUMN last_checked_at TIMESTAMPTZ;
ALTER TABLE slots ADD COLUMN last_outcome TEXT;
ALTER TABLE slots DROP CONSTRAINT slots_test_case_key_key;
ALTER TABLE slots ADD CONSTRAINT slots_identity_key UNIQUE (team_id, test_case_key, environment);
ALTER TABLE slots ADD CONSTRAINT slots_environment_fkey
    FOREIGN KEY (environment) REFERENCES environments(name);
CREATE INDEX slots_team_id_idx ON slots (team_id);

CREATE TABLE scan_run_items (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id          UUID NOT NULL REFERENCES scan_runs(id) ON DELETE CASCADE,
    slot_id         UUID REFERENCES slots(id) ON DELETE SET NULL,
    test_case_key   TEXT NOT NULL,
    environment     TEXT NOT NULL,
    outcome         TEXT NOT NULL CHECK (outcome IN
                        ('valid', 'invalid', 'regenerated', 'generated', 'generation_failed',
                         'check_error', 'block_error', 'orphaned')),
    old_pnr         TEXT,
    new_pnr         TEXT,
    failed_rule     TEXT,
    error           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX scan_run_items_run_id_idx ON scan_run_items (run_id);

-- Instances: linked to the runs that created and retired them (the design
-- doc's debugging trail), and to the block they were generated for, so an
-- edited requirement can be detected.
ALTER TABLE instances ADD COLUMN created_by_run_id UUID REFERENCES scan_runs(id) ON DELETE SET NULL;
ALTER TABLE instances ADD COLUMN retired_by_run_id UUID REFERENCES scan_runs(id) ON DELETE SET NULL;
ALTER TABLE instances ADD COLUMN block_hash TEXT;
ALTER TABLE instances DROP CONSTRAINT instances_retire_reason_check;
ALTER TABLE instances ADD CONSTRAINT instances_retire_reason_check CHECK (retire_reason IN
    ('consumed', 'expired', 'dead', 'manual', 'requirement_changed', 'generation_failed'));
CREATE INDEX instances_created_at_idx ON instances (created_at);
