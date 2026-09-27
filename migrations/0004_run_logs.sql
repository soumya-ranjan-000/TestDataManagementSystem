-- The step-by-step activity log of every run: what the sync found, each
-- PSS check and rule result, every retirement and generation step. The run
-- summary and per-test-case items stay forever; log lines are pruned after a
-- retention period because a large team produces tens of thousands a day.

CREATE TABLE scan_run_logs (
    id              BIGSERIAL PRIMARY KEY,   -- insertion order is log order
    run_id          UUID NOT NULL REFERENCES scan_runs(id) ON DELETE CASCADE,
    at              TIMESTAMPTZ NOT NULL,
    level           TEXT NOT NULL CHECK (level IN ('info', 'warn', 'error')),
    test_case_key   TEXT,
    environment     TEXT,
    message         TEXT NOT NULL
);
CREATE INDEX scan_run_logs_run_idx ON scan_run_logs (run_id, id);
CREATE INDEX scan_run_logs_at_idx ON scan_run_logs (at);
