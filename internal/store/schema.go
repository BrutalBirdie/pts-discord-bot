package store

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id_hash bytea PRIMARY KEY,
    username text NOT NULL,
    display_name text NOT NULL,
    is_admin boolean NOT NULL,
    csrf_token text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS oidc_logins (
    state_hash bytea PRIMARY KEY,
    verifier text NOT NULL,
    nonce text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS audit_log (
    id bigserial PRIMARY KEY,
    at timestamptz NOT NULL DEFAULT now(),
    actor text NOT NULL,
    action text NOT NULL,
    target text NOT NULL,
    detail text NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS user_prefs (
    username text PRIMARY KEY,
    show_labels boolean NOT NULL DEFAULT false
);
CREATE TABLE IF NOT EXISTS settings (
    key text PRIMARY KEY,
    value text NOT NULL
);
CREATE TABLE IF NOT EXISTS media (
    id bigserial PRIMARY KEY,
    name text NOT NULL UNIQUE,
    kind text NOT NULL CHECK (kind IN ('sound', 'image', 'video', 'file')),
    content_type text NOT NULL DEFAULT '',
    size_bytes bigint NOT NULL,
    duration_ms integer NOT NULL DEFAULT 0,
    width integer NOT NULL DEFAULT 0,
    height integer NOT NULL DEFAULT 0,
    uploaded_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS messages (
    id bigserial PRIMARY KEY,
    name text NOT NULL,
    content text NOT NULL DEFAULT '',
    embeds jsonb NOT NULL DEFAULT '[]',
    files bigint[] NOT NULL DEFAULT '{}',
    created_by text NOT NULL,
    updated_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS tasks (
    id bigserial PRIMARY KEY,
    name text NOT NULL,
    guild_id text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    busy text NOT NULL DEFAULT 'wait' CHECK (busy IN ('wait', 'skip')),
    trigger jsonb NOT NULL,
    steps jsonb NOT NULL,
    paused_reason text NOT NULL DEFAULT '',
    created_by text NOT NULL,
    updated_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS runs (
    id bigserial PRIMARY KEY,
    task_id bigint REFERENCES tasks (id) ON DELETE CASCADE,
    title text NOT NULL,
    guild_id text NOT NULL DEFAULT '',
    source text NOT NULL CHECK (source IN ('schedule', 'manual')),
    actor text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'failed', 'skipped')),
    failed_step integer,
    error text NOT NULL DEFAULT '',
    detail text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS runs_task ON runs (task_id, id DESC);
CREATE INDEX IF NOT EXISTS runs_started ON runs (started_at DESC);
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS groups text[] NOT NULL DEFAULT '{}';
ALTER TABLE media ADD COLUMN IF NOT EXISTS tags text[] NOT NULL DEFAULT '{}';
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS slash boolean NOT NULL DEFAULT false;
ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_source_check;
ALTER TABLE runs ADD CONSTRAINT runs_source_check CHECK (source IN ('schedule', 'manual', 'event', 'discord'));`
