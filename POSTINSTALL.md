The bot needs a Discord bot token before it connects.

1. Create an application in the [Discord Developer Portal](https://discord.com/developers/applications). Under **Bot**, click **Reset Token** and copy the token. On the same page, switch on **Server Members Intent**, and **Message Content Intent** if tasks should react to keywords in messages.
2. Set `DISCORD_TOKEN` in `/app/data/env.sh`. Add your Cloudron username to `BOT_ADMINS` there, or a group to `BOT_ADMIN_GROUP`. Restart the app.
3. Open the app, go to **Servers** and add the bot to your Discord server with the invite link.
4. Upload sounds under **Media** and create a task under **Tasks**.
