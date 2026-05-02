package db

const schemaSQL = `
CREATE TABLE IF NOT EXISTS repos (
    id              TEXT PRIMARY KEY,
    default_branch  TEXT,
    source          TEXT NOT NULL DEFAULT 'config',
    last_poll_at    INTEGER,
    last_triaged_refresh_at INTEGER,
    created_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS items (
    id              TEXT PRIMARY KEY,
    repo_id         TEXT NOT NULL REFERENCES repos(id),
    kind            TEXT NOT NULL,
    role            TEXT NOT NULL DEFAULT 'maintainer',
    number          INTEGER NOT NULL,
    title           TEXT,
    author          TEXT,
    state           TEXT,
    is_draft        INTEGER NOT NULL DEFAULT 0,
    gh_triaged      INTEGER NOT NULL DEFAULT 0,
    waiting_on      TEXT,
    last_event_at   INTEGER,
    stale_since     INTEGER,
    last_seen_updated_at  INTEGER,
    last_seen_comment_id  INTEGER,
    last_self_activity_at INTEGER,
    head_repo       TEXT,
    head_ref        TEXT,
    head_clone_url  TEXT,
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
    rerun_instructions TEXT,
    created_at       INTEGER NOT NULL,
    created_at_nanos INTEGER,
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

CREATE TABLE IF NOT EXISTS fix_jobs (
    id                TEXT PRIMARY KEY,
    item_id           TEXT NOT NULL,
    recommendation_id TEXT NOT NULL,
    option_id         TEXT,
    repo_id           TEXT NOT NULL,
    item_number       INTEGER NOT NULL,
    item_kind         TEXT NOT NULL,
    title             TEXT,
    fix_prompt        TEXT NOT NULL,
    agent             TEXT,
    pr_create         TEXT NOT NULL,
    branch            TEXT,
    worktree_path     TEXT,
    pr_url            TEXT,
    status            TEXT NOT NULL,
    phase             TEXT,
    message           TEXT,
    error             TEXT,
    created_at        INTEGER NOT NULL,
    started_at        INTEGER,
    updated_at        INTEGER NOT NULL,
    completed_at      INTEGER
);

CREATE INDEX IF NOT EXISTS idx_fix_jobs_item_active
    ON fix_jobs(item_id, status);

CREATE INDEX IF NOT EXISTS idx_fix_jobs_rec_option
    ON fix_jobs(recommendation_id, option_id);
`
