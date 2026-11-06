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
ALTER TABLE runs ADD CONSTRAINT runs_source_check CHECK (source IN ('schedule', 'manual', 'event', 'discord'));
CREATE TABLE IF NOT EXISTS preview_volume (
    username text NOT NULL,
    media_id bigint NOT NULL,
    volume integer NOT NULL CHECK (volume BETWEEN 0 AND 100),
    PRIMARY KEY (username, media_id)
);
ALTER TABLE user_prefs ADD COLUMN IF NOT EXISTS confirm_actions boolean NOT NULL DEFAULT true;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS deleted_at timestamptz;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS deleted_by text;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS deleted_at timestamptz;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS deleted_by text;
ALTER TABLE media ADD COLUMN IF NOT EXISTS deleted_at timestamptz;
ALTER TABLE media ADD COLUMN IF NOT EXISTS deleted_by text;
ALTER TABLE media DROP CONSTRAINT IF EXISTS media_name_key;
CREATE UNIQUE INDEX IF NOT EXISTS media_name_live ON media (name) WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS bot_profile_changes (
    guild_id text NOT NULL,
    field text NOT NULL,
    changed_at timestamptz,
    blocked_until timestamptz,
    PRIMARY KEY (guild_id, field)
);`
