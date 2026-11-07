#!/bin/bash
# End-to-end test without Discord: fresh install, real browser, mock OIDC, PostgreSQL.
# Usage: test/e2e/run.sh   (needs Docker)
# ARTIFACTS=<dir> keeps failed pages and the app log, KEEP=1 keeps the containers

set -eu

cd "$(dirname "$0")"
readonly root=$(cd ../.. && pwd)
readonly img=ptsbot:e2e
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

echo "==> Admin added to BOT_ADMINS applies to a running session"
docker exec "${app}" sed -i 's/^export BOT_ADMINS=.*/export BOT_ADMINS="carol.smith, bob"/' /app/data/env.sh
docker restart "${app}" >/dev/null
wait_healthy
browser promoted

if (( failures > 0 )); then
    echo "==> ${failures} failure(s)"
    exit 1
fi
echo "==> All passed"
