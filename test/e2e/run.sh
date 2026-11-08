#!/bin/bash
# End-to-end test without Discord: fresh install, real browser, mock OIDC, PostgreSQL.
# Usage: test/e2e/run.sh   (needs Docker)
# ARTIFACTS=<dir> keeps failed pages and the app log, KEEP=1 keeps the containers

set -eu

cd "$(dirname "$0")"
readonly root=$(cd ../.. && pwd)
readonly img=ptsbot:e2e
readonly build_img=ptsbot:e2e-build
readonly net=ptse2e
readonly app=ptse2e-app
readonly mock=ptse2e-mock
readonly db=ptse2e-db
readonly work=$(mktemp -d)
failures=0

cleanup_containers() {
    docker rm -f "${app}" "${mock}" "${db}" >/dev/null 2>&1 || true
    docker volume rm ptse2e-data >/dev/null 2>&1 || true
    docker network rm "${net}" >/dev/null 2>&1 || true
}
cleanup() {
    if [[ -n "${ARTIFACTS:-}" ]]; then
        mkdir -p "${ARTIFACTS}"
        docker logs "${app}" > "${ARTIFACTS}/app.log" 2>&1 || true
        cp "${work}/"fail-*.html "${ARTIFACTS}/" 2>/dev/null || true
    fi
    if [[ -n "${KEEP:-}" ]]; then
        echo "==> Kept containers and ${work}"
    else
        cleanup_containers
        rm -rf "${work}"
    fi
}
cleanup_containers
trap cleanup EXIT

pass() { echo "PASS [shell] $1"; }
fail() { echo "FAIL [shell] $1 :: $2"; failures=$((failures + 1)); }
expect() { # expect <name> <regex> <output>
    if [[ "$3" =~ $2 ]]; then pass "$1"; else fail "$1" "${3:0:300}"; fi
}

browser() {
    docker run --rm --network "container:${app}" -u "$(id -u):$(id -g)" -e HOME=/tmp -v "${PWD}:/e2e" -v "${work}:/work" -w /e2e \
        mcr.microsoft.com/playwright:v1.65.0-noble node e2e.mjs "$1" || failures=$((failures + 1))
}

wait_healthy() {
    for _ in $(seq 1 60); do
        docker exec "${app}" curl -sf http://localhost:8000/healthz >/dev/null 2>&1 && return 0
        sleep 1
    done
    docker logs "${app}" | tail -30
    return 1
}

echo "==> Building"
docker build -q -t "${img}" "${root}" >/dev/null
docker build -q --target build -t "${build_img}" "${root}" >/dev/null
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "${PWD}:/e2e" -w /e2e mcr.microsoft.com/playwright:v1.65.0-noble npm install --no-audit --no-fund --silent
chmod 0777 "${work}"

docker network create "${net}" >/dev/null
docker run -d --name "${mock}" --network "${net}" -e JSON_CONFIG='{"interactiveLogin":true}' ghcr.io/navikt/mock-oauth2-server:6.0.3 >/dev/null
docker run -d --name "${db}" --network "${net}" -e POSTGRES_USER=bot -e POSTGRES_PASSWORD=botpw -e POSTGRES_DB=bot postgres:17-alpine >/dev/null

# a sound from the old /app/data/sounds directory, imported into the media library on start
docker volume create ptse2e-data >/dev/null
docker run --rm -v ptse2e-data:/app/data "${img}" sh -c 'mkdir -p /app/data/sounds && ffmpeg -loglevel error -f lavfi -i sine=frequency=440:duration=2 -c:a libopus /app/data/sounds/beep.ogg'

echo "==> Fresh install"
docker run -d --name "${app}" --network "${net}" --read-only --tmpfs /run --tmpfs /tmp -v ptse2e-data:/app/data \
    -e CLOUDRON_APP_ORIGIN=http://localhost:8000 -e CLOUDRON_POSTGRESQL_URL="postgres://bot:botpw@${db}:5432/bot" \
    -e CLOUDRON_OIDC_ISSUER="http://${mock}:8080/default" -e CLOUDRON_OIDC_CLIENT_ID=ptsbot -e CLOUDRON_OIDC_CLIENT_SECRET=secret \
    "${img}" >/dev/null
wait_healthy
expect "env.sh created on first start" 'BOT_ADMIN_GROUP="\$\{BOT_ADMIN_GROUP:-\}"' "$(docker exec "${app}" cat /app/data/env.sh)"
expect "old sounds imported" "imported sound into the media library.*beep.ogg" "$(docker logs "${app}" 2>&1)"
expect "sounds directory removed" "^gone$" "$(docker exec "${app}" sh -c 'test -e /app/data/sounds || echo gone')"
expect "no token, no crash" "DISCORD_TOKEN is not set|web ui listening" "$(docker logs "${app}" 2>&1)"

echo "==> Store and web tests against PostgreSQL"
docker exec "${db}" psql -U bot -d bot -XAtc "CREATE DATABASE storetest" >/dev/null
docker exec "${db}" psql -U bot -d bot -XAtc "CREATE DATABASE webtest" >/dev/null
docker run --rm --network "${net}" -e TEST_DATABASE_URL="postgres://bot:botpw@${db}:5432/storetest" "${build_img}" go test -count=1 ./internal/store/ \
    && pass "store tests" || fail "store tests" "see the output above"
docker run --rm --network "${net}" -e TEST_DATABASE_URL="postgres://bot:botpw@${db}:5432/webtest" "${build_img}" go test -count=1 ./internal/web/ \
    && pass "web tests" || fail "web tests" "see the output above"

docker exec "${app}" sed -i 's/^export BOT_ADMIN_GROUP=.*/export BOT_ADMIN_GROUP="bot-admins"/' /app/data/env.sh
docker restart "${app}" >/dev/null
wait_healthy

browser admin
browser user
browser layout

if [[ -n "${SCREENSHOTS:-}" ]]; then
    browser shots
    mkdir -p "${root}/screenshots"
    cp "${work}/shots/"*.png "${root}/screenshots/"
fi

echo "==> Restart keeps data, runs missed while stopped are recorded"
docker stop "${app}" >/dev/null
psql() { docker exec "${db}" psql -U bot -d bot -XAtc "$1"; }
psql "INSERT INTO tasks (name, guild_id, trigger, steps, created_by, updated_by) VALUES
  ('Every minute', '1', '{\"kind\":\"cron\",\"cron\":[\"* * * * *\"],\"timezone\":\"UTC\"}', '[{\"type\":\"leave\"}]', 'e2e', 'e2e'),
  ('Greeting', '1', '{\"kind\":\"event\",\"event\":\"voice_join\",\"cooldown\":45,\"user_ids\":[\"333333333333333333\"],\"timezone\":\"UTC\"}', '[{\"type\":\"dm\",\"to_trigger\":true,\"text\":\"Hi {member}\"}]', 'e2e', 'e2e')" >/dev/null
psql "UPDATE settings SET value = to_char((now() - interval '10 minutes') AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') WHERE key = 'heartbeat'" >/dev/null
docker start "${app}" >/dev/null
wait_healthy
missed=$(psql "SELECT count(*) FROM runs r JOIN tasks t ON t.id = r.task_id WHERE t.name = 'Every minute' AND r.status = 'skipped'")
expect "missed runs recorded" "^(9|10)$" "${missed}"
expect "event tasks are not counted as missed" "^0$" "$(psql "SELECT count(*) FROM runs r JOIN tasks t ON t.id = r.task_id WHERE t.name = 'Greeting'")"
browser persisted
browser trash
browser uploads
psql "INSERT INTO messages (name, created_by, updated_by) SELECT 'Bulk ' || lpad(n::text, 2, '0'), 'e2e', 'e2e' FROM generate_series(1, 30) n" >/dev/null
browser paging
psql "INSERT INTO messages (name, created_by, updated_by, deleted_at, deleted_by) VALUES ('Old trash', 'e2e', 'e2e', now() - interval '31 days', 'e2e'), ('New trash', 'e2e', 'e2e', now(), 'e2e')" >/dev/null

echo "==> Admin added to BOT_ADMINS applies to a running session"
docker exec "${app}" sed -i 's/^export BOT_ADMINS=.*/export BOT_ADMINS="carol.smith, bob"/' /app/data/env.sh
docker restart "${app}" >/dev/null
wait_healthy
for _ in $(seq 1 10); do
    [[ "$(psql "SELECT count(*) FROM messages WHERE name = 'Old trash'")" == 0 ]] && break
    sleep 1
done
expect "trash older than the retention purged" "^New trash$" "$(psql "SELECT string_agg(name, ',') FROM messages WHERE name LIKE '% trash'")"
browser promoted

echo "==> Discord access"
psql "INSERT INTO discord_users (user_id, username, global_name) VALUES
  ('111111111111111111', 'mira.k', 'Mira'), ('222222222222222222', 'free_nitro_x', 'spam'), ('333333333333333334', 'ann', 'ann'), ('444444444444444444', 'kim', 'Kim');
UPDATE discord_users SET blocked = true, blocked_by = 'ann', blocked_at = now() WHERE user_id = '222222222222222222';
INSERT INTO access_requests (user_id, guild_ids, note, requested_at, expires_at) VALUES
  ('111111111111111111', '{1,2}', 'I run the raid nights.', now() - interval '6 days', now() + interval '1 day'),
  ('444444444444444444', '{1}', '', now() - interval '8 days', now() - interval '1 day');
INSERT INTO access_requests (user_id, guild_ids, status, decided_by, decided_at) VALUES ('333333333333333334', '{1}', 'approved', 'ann', now());
INSERT INTO discord_grants (user_id, guild_id, granted_by) VALUES ('333333333333333334', '1', 'ann');
INSERT INTO sessions (id_hash, username, display_name, is_admin, groups, csrf_token, provider, guild_ids)
  VALUES (sha256('e2e-discord-session'::bytea), 'discord:333333333333333334', 'ann', true, '{bot-admins}', 'e2e', 'discord', '{1,2}');
INSERT INTO tasks (name, guild_id, enabled, trigger, steps, created_by, updated_by) VALUES
  ('Guild one task', '1', false, '{\"kind\":\"manual\",\"timezone\":\"UTC\"}', '[{\"type\":\"leave\"}]', 'e2e', 'e2e'),
  ('Guild two task', '2', false, '{\"kind\":\"manual\",\"timezone\":\"UTC\"}', '[{\"type\":\"leave\"}]', 'e2e', 'e2e');
INSERT INTO messages (name, guild_id, created_by, updated_by) VALUES ('Guild one msg', '1', 'e2e', 'e2e'), ('Guild two msg', '2', 'e2e', 'e2e');
INSERT INTO messages (name, guild_id, created_by, updated_by, deleted_at, deleted_by) VALUES ('Trash one', '1', 'e2e', 'e2e', now(), 'e2e'), ('Trash two', '2', 'e2e', 'e2e', now(), 'e2e');
INSERT INTO media (name, kind, size_bytes, uploaded_by, guild_id) VALUES ('guild-one.ogg', 'sound', 1, 'e2e', '1'), ('guild-two.ogg', 'sound', 1, 'e2e', '2');
INSERT INTO media (name, kind, size_bytes, uploaded_by) VALUES ('shared-horn.ogg', 'sound', 1, 'e2e');
INSERT INTO tasks (name, guild_id, enabled, trigger, steps, created_by, updated_by)
  SELECT 'Shared horn task', '1', false, '{\"kind\":\"manual\",\"timezone\":\"UTC\"}', jsonb_build_array(jsonb_build_object('type', 'play', 'media_ids', jsonb_build_array(id), 'volume', 100)), 'e2e', 'e2e'
  FROM media WHERE name = 'shared-horn.ogg'" >/dev/null
docker exec "${app}" sed -i 's/^export DISCORD_CLIENT_SECRET=.*/export DISCORD_CLIENT_SECRET="e2e-secret"/' /app/data/env.sh
docker restart "${app}" >/dev/null
wait_healthy
for _ in $(seq 1 10); do
    [[ "$(psql "SELECT status FROM access_requests WHERE user_id = '444444444444444444'")" == denied ]] && break
    sleep 1
done
expect "expired request denied automatically" "^denied\|system$" "$(psql "SELECT status || '|' || decided_by FROM access_requests WHERE user_id = '444444444444444444'")"
expect "pending request not expired" "^pending$" "$(psql "SELECT status FROM access_requests WHERE user_id = '111111111111111111'")"
browser access
expect "approved servers granted" "^1$" "$(psql "SELECT string_agg(guild_id, ',') FROM discord_grants WHERE user_id = '111111111111111111'")"
expect "secret not logged" "^0$" "$(docker logs "${app}" 2>&1 | grep -c e2e-secret || true)"

if (( failures > 0 )); then
    echo "==> ${failures} failure(s)"
    exit 1
fi
echo "==> All passed"
