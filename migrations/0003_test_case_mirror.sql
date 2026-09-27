-- A mirror of each team's test cases and folder tree from its test
-- management app, refreshed on every sync. It holds every test case in the
-- configured folder — with or without a TDMS block — so the UI can browse,
-- search and page through thousands of them without calling QMetry.

CREATE TABLE tcm_folders (
    team_id     UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    folder_id   BIGINT NOT NULL,
    parent_id   BIGINT,               -- NULL for the root of the team's configured subtree
    name        TEXT NOT NULL,
    path        TEXT NOT NULL,        -- e.g. 'chatbot-booking-retrival/regression'
    position    INT NOT NULL,         -- depth-first order, for rendering the tree
    depth       INT NOT NULL,         -- 0 for the subtree's root
    PRIMARY KEY (team_id, folder_id)
);

CREATE TABLE test_cases (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id           UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    tcm_test_case_id  TEXT NOT NULL,
    test_case_key     TEXT NOT NULL,  -- e.g. 'ACP-TC-12'
    key_num           INT NOT NULL DEFAULT 0, -- the key's trailing number, so ACP-TC-2 sorts before ACP-TC-10
    summary           TEXT NOT NULL DEFAULT '',
    tcm_version       INT NOT NULL DEFAULT 1,
    folder_ids        BIGINT[] NOT NULL DEFAULT '{}',
    raw_block         TEXT NOT NULL DEFAULT '', -- the TDMS block exactly as QA wrote it; '' = none
    has_block         BOOLEAN GENERATED ALWAYS AS (btrim(raw_block) <> '') STORED,
    synced_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    removed_at        TIMESTAMPTZ,    -- no longer in the configured folder
    UNIQUE (team_id, test_case_key)
);
CREATE INDEX test_cases_page_idx ON test_cases (team_id, key_num, test_case_key) WHERE removed_at IS NULL;
CREATE INDEX test_cases_folders_idx ON test_cases USING gin (folder_ids);

-- The test data a PNR record hands to a test: a list (one entry for now) of
-- {pnr, last_name}, since retrieving a booking needs both.
ALTER TABLE instances ADD COLUMN test_data JSONB NOT NULL DEFAULT '[]';
