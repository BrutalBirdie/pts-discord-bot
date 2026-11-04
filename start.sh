#!/bin/bash

set -eu

mkdir -p /app/data/sounds

if [[ ! -f /app/data/env.sh ]]; then
    echo "==> Creating /app/data/env.sh"
    api_token=$(tr -dc 'a-f0-9' < /dev/urandom | head -c 64)
    sed "s/^export API_TOKEN=\"\"/export API_TOKEN=\"${api_token}\"/" /app/pkg/env.sh.template > /app/data/env.sh
fi

source /app/data/env.sh
export SOUNDS_DIR=/app/data/sounds
export LISTEN_ADDR=:8000

chown -R cloudron:cloudron /app/data

if [[ -z "${DISCORD_TOKEN}" ]]; then
    echo "==> DISCORD_TOKEN is not set. Run 'cloudron env set DISCORD_TOKEN=...' or edit /app/data/env.sh, then restart the app"
    exec sleep infinity
fi

echo "==> Starting voicebot"
exec gosu cloudron:cloudron /app/code/voicebot
