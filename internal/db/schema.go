package db

const schemaSQL = `
CREATE TABLE IF NOT EXISTS repos (
    id              TEXT PRIMARY KEY,
    default_branch  TEXT,
    last_poll_at    INTEGER,
    last_triaged_refresh_at INTEGER,
    created_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS items (
    id              TEXT PRIMARY KEY,
    repo_id         TEXT NOT NULL REFERENCES repos(id),
    kind            TEXT NOT NULL,
    number          INTEGER NOT NULL,
    title           TEXT,
    author          TEXT,
    state           TEXT,
    is_draft        INTEGER NOT NULL DEFAULT 0,
    gh_triaged      INTEGER NOT NULL DEFAULT 0,
    waiting_on      TEXT,
    last_event_at   INTEGER,
    stale_since     INTEGER,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- A recommendation is a single agent run on an item. Per-action fields
-- (state_change, rationale, draft_comment, etc.) live on
-- recommendation_options - each row in that table is one alternative the
-- agent proposed. The agent is encouraged to return 2-3 options whenever
-- there are multiple reasonable next steps, and a single option only
-- when there's truly one obvious resolution.
CREATE TABLE IF NOT EXISTS recommendations (
    id               TEXT PRIMARY KEY,
    item_id          TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    agent            TEXT NOT NULL,
    model            TEXT,
    rationale        TEXT,
    draft_comment    TEXT,
    followups        TEXT,
    proposed_labels  TEXT,
    state_change     TEXT,
    confidence       TEXT,
    tokens_in        INTEGER,
    tokens_out       INTEGER,
    created_at       INTEGER NOT NULL,
    superseded_at    INTEGER
);

CREATE TABLE IF NOT EXISTS recommendation_options (
    id                TEXT PRIMARY KEY,
    recommendation_id TEXT NOT NULL REFERENCES recommendations(id) ON DELETE CASCADE,
    position          INTEGER NOT NULL,
    state_change      TEXT,
    rationale         TEXT,
    draft_comment     TEXT,
    fix_prompt        TEXT,
    proposed_labels   TEXT,
    confidence        TEXT,
    waiting_on        TEXT,
    followups         TEXT,
    created_at        INTEGER NOT NULL,
    UNIQUE (recommendation_id, position)
);

CREATE INDEX IF NOT EXISTS idx_recommendation_options_rec
    ON recommendation_options(recommendation_id, position);

CREATE TABLE IF NOT EXISTS approvals (
    id                 TEXT PRIMARY KEY,
    recommendation_id  TEXT NOT NULL REFERENCES recommendations(id) ON DELETE CASCADE,
    option_id          TEXT REFERENCES recommendation_options(id) ON DELETE SET NULL,
    decision           TEXT NOT NULL,
    final_comment      TEXT,
    final_labels       TEXT,
    final_state_change TEXT,
    acted_at           INTEGER,
    acted_error        TEXT,
    created_at         INTEGER NOT NULL
);
`
