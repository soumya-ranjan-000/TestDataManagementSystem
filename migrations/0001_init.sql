-- TDMS core schema. Slots are permanent; instances are disposable.
-- See "Slots and instances" and "Storage and database" in the design doc:
-- definitions in version control (this file), authored data in the test
-- case (QMetry), running data in this database.

CREATE TABLE slots (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    test_case_key   TEXT NOT NULL UNIQUE,      -- e.g. 'ACP-TC-1'
    qmetry_tc_id    TEXT NOT NULL,             -- QMetry's opaque test case id
    class           TEXT NOT NULL CHECK (class IN ('OWNED', 'SHARED')),
    environment     TEXT NOT NULL,
    block           JSONB NOT NULL,            -- the ingested requirement+validity block
    block_hash      TEXT NOT NULL,             -- drives change detection; see ingest.Hash
    orphaned_at     TIMESTAMPTZ,               -- set when the test case disappears from QMetry
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE instances (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slot_id         UUID NOT NULL REFERENCES slots(id),
    pnr             TEXT,
    state           TEXT NOT NULL CHECK (state IN
                        ('GENERATING', 'VALID', 'CONSUMED', 'EXPIRED', 'DEAD', 'RETIRED')),
    retire_reason   TEXT CHECK (retire_reason IN ('consumed', 'expired', 'dead')),
    reserved_by     TEXT,                      -- run/test identity holding this instance (atomic reservation)
    reserved_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at      TIMESTAMPTZ
);

-- A slot has at most one non-retired instance at a time — "a slot always
-- holds one current instance plus a history of retired ones."
CREATE UNIQUE INDEX one_live_instance_per_slot
    ON instances (slot_id)
    WHERE state <> 'RETIRED';

CREATE INDEX instances_slot_id_idx ON instances (slot_id);
CREATE INDEX slots_environment_idx ON slots (environment);
