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
ALTER TABLE user_prefs ADD COLUMN IF NOT EXISTS confirm_actions boolean NOT NULL DEFAULT true;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS deleted_at timestamptz;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS deleted_by text;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS deleted_at timestamptz;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS deleted_by text;
ALTER TABLE media ADD COLUMN IF NOT EXISTS deleted_at timestamptz;
ALTER TABLE media ADD COLUMN IF NOT EXISTS deleted_by text;
ALTER TABLE media DROP CONSTRAINT IF EXISTS media_name_key;
CREATE TABLE IF NOT EXISTS bot_profile_changes (
    guild_id text NOT NULL,
    field text NOT NULL,
    changed_at timestamptz,
    blocked_until timestamptz,
    PRIMARY KEY (guild_id, field)
);
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS provider text NOT NULL DEFAULT 'cloudron';
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS guild_ids text[] NOT NULL DEFAULT '{}';
ALTER TABLE oidc_logins ADD COLUMN IF NOT EXISTS provider text NOT NULL DEFAULT 'cloudron';
CREATE TABLE IF NOT EXISTS discord_users (
    user_id text PRIMARY KEY,
    username text NOT NULL,
    global_name text NOT NULL DEFAULT '',
    avatar text NOT NULL DEFAULT '',
    blocked boolean NOT NULL DEFAULT false,
    blocked_by text,
    blocked_at timestamptz,
    last_login_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS access_requests (
    id bigserial PRIMARY KEY,
    user_id text NOT NULL REFERENCES discord_users (user_id) ON DELETE CASCADE,
    guild_ids text[] NOT NULL,
    note text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied', 'revoked')),
    requested_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT now() + interval '7 days',
    decided_by text,
    decided_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS access_requests_one_pending ON access_requests (user_id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS access_requests_user ON access_requests (user_id, id DESC);
CREATE TABLE IF NOT EXISTS discord_grants (
    user_id text NOT NULL REFERENCES discord_users (user_id) ON DELETE CASCADE,
    guild_id text NOT NULL,
    granted_by text NOT NULL,
    granted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, guild_id)
);
ALTER TABLE messages ADD COLUMN IF NOT EXISTS guild_id text;
ALTER TABLE media ADD COLUMN IF NOT EXISTS guild_id text;
DROP INDEX IF EXISTS media_name_live;
CREATE INDEX IF NOT EXISTS runs_guild ON runs (guild_id, id DESC);
ALTER TABLE media ADD COLUMN IF NOT EXISTS group_only boolean NOT NULL DEFAULT false;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS group_only boolean NOT NULL DEFAULT false;
ALTER TABLE user_prefs ADD COLUMN IF NOT EXISTS preview_volume integer NOT NULL DEFAULT 100 CHECK (preview_volume BETWEEN 0 AND 100);
DROP TABLE IF EXISTS preview_volume;
ALTER TABLE media ADD COLUMN IF NOT EXISTS emoji text NOT NULL DEFAULT '';
ALTER TABLE media ADD COLUMN IF NOT EXISTS icon_media_id bigint REFERENCES media (id) ON DELETE SET NULL;
CREATE TABLE IF NOT EXISTS favorite_media (
    username text NOT NULL,
    media_id bigint NOT NULL REFERENCES media (id) ON DELETE CASCADE,
    PRIMARY KEY (username, media_id)
);
DO $$
DECLARE
    r record;
    n integer;
    try text;
BEGIN
    IF to_regclass('media_live_name') IS NOT NULL THEN
        RETURN;
    END IF;
    FOR r IN SELECT id, name FROM (SELECT id, name, row_number() OVER (PARTITION BY name ORDER BY guild_id NULLS FIRST, id) AS k
        FROM media WHERE deleted_at IS NULL) d WHERE k > 1 ORDER BY id LOOP
        n := 2;
        LOOP
            try := regexp_replace(r.name, '^(.+?)(\.[^. ()]+)?$', '\1 (' || n || ')\2');
            EXIT WHEN NOT EXISTS (SELECT FROM media WHERE name = try AND deleted_at IS NULL);
            n := n + 1;
        END LOOP;
        UPDATE media SET name = try WHERE id = r.id;
    END LOOP;
    DROP INDEX IF EXISTS media_owner_name_live;
    CREATE UNIQUE INDEX media_live_name ON media (name) WHERE deleted_at IS NULL;
END $$;`
