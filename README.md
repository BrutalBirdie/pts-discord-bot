# pts-discord-bot

Cloudron app with a Discord bot that joins a voice channel and plays a sound file when n8n calls its HTTP API.

## Install

```sh
cloudron install --location voicebot
cloudron env set --app voicebot DISCORD_TOKEN=<bot token>
cloudron exec --app voicebot -- grep API_TOKEN /app/data/env.sh
```

Other settings live in `/app/data/env.sh` (File Manager in the dashboard). Restart the app after editing it.

Any format ffmpeg can read works (`.ogg`, `.mp3`, `.wav`, ...).

## API

All endpoints except `/healthz` need `Authorization: Bearer <API_TOKEN>`.

| Method | Path | |
|---|---|---|
| `POST` | `/play` | Play a sound, see below |
| `GET` | `/sounds` | List files in `/app/data/sounds` |
| `GET` | `/healthz` | |

`/play` accepts the sound in three ways, picked by `Content-Type`:

| Content-Type | Sound | IDs |
|---|---|---|
| any audio or `application/octet-stream` | request body | query `?channel_id=...&guild_id=...` |
| `multipart/form-data` | file field `file` | form fields `channel_id`, `guild_id` |
| `application/json` | `"file"`: name in `/app/data/sounds` | `"channel_id"`, `"guild_id"` as strings |

`channel_id` falls back to `DEFAULT_CHANNEL_ID`; the guild is looked up from the channel. Uploads are limited to `MAX_UPLOAD_MB` (default 50). `/play` returns after playback has finished, `409` if the bot is already playing in that guild.

```sh
curl -X POST "https://voicebot.example.com/play?channel_id=475645920659243008" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: audio/ogg" \
  --data-binary @allah_short.ogg
```

## n8n

1. Import `n8n/adhan-voice.workflow.json` and point the `Read sound file` node at the file on the n8n side.
2. Create a **Header Auth** credential: name `Authorization`, value `Bearer <API_TOKEN>`, and select it in the HTTP Request node.
3. Set the node URL to `https://voicebot.<your domain>/play`.
4. Click **Execute workflow** to test via the `Test now` trigger, then activate the workflow.

The schedule runs in `Europe/Berlin` (workflow setting) at 05:00, 11:50, 14:00, 16:35 and 18:15.

## Discord bot permissions

The bot needs **View Channel**, **Connect** and **Speak** on the voice channel.
