#!/bin/bash

set -eu

mkdir -p /app/data/media

if [[ ! -f /app/data/env.sh ]]; then
    echo "==> Creating /app/data/env.sh"
    cp /app/pkg/env.sh.template /app/data/env.sh
elif ! grep -q BOT_ADMINS /app/data/env.sh; then
    echo "==> Adding the web UI settings to /app/data/env.sh"
    sed -n '/^# Cloudron usernames/,$p' /app/pkg/env.sh.template >> /app/data/env.sh
fi
if ! grep -q DISCORD_CLIENT_SECRET /app/data/env.sh; then
    echo "==> Adding the Discord login setting to /app/data/env.sh"
    sed -n '/^# Discord login/,$p' /app/pkg/env.sh.template >> /app/data/env.sh
fi

source /app/data/env.sh

chown -R cloudron:cloudron /app/data

echo "==> Starting PTS Discord Bot"
exec gosu cloudron:cloudron /app/code/voicebot
