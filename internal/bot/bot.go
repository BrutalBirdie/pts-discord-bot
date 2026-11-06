// Package bot owns the Discord connection: gateway, caches, voice and sending messages.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo"
	disgobot "github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
)

var ErrNotConnected = errors.New("the bot is not connected to Discord")

// Permissions the invite link asks for.
const InvitePermissions = discord.PermissionViewChannel | discord.PermissionSendMessages | discord.PermissionEmbedLinks |
	discord.PermissionAttachFiles | discord.PermissionConnect | discord.PermissionSpeak | discord.PermissionChangeNickname

type Config struct {
	Token         string
	MembersIntent bool // GUILD_MEMBERS, privileged
	MessageIntent bool // MESSAGE_CONTENT, privileged, for keyword triggers
	Voice         []voice.ManagerConfigOpt
	Logger        *slog.Logger
}

type Bot struct {
	cfg Config
	log *slog.Logger

	mu          sync.RWMutex
	client      *disgobot.Client
	members     bool
	messages    bool
	handler     atomic.Pointer[Handler]
	onLeave     atomic.Pointer[func(string)]
	lastErr     string
	connectedAt time.Time

	vmu     sync.Mutex
	playing map[snowflake.ID]*Playing

	waiters voiceWaiters
	limits  profileLimits
}

func New(cfg Config) *Bot {
	return &Bot{cfg: cfg, log: cfg.Logger, members: cfg.MembersIntent, messages: cfg.MessageIntent, playing: map[snowflake.ID]*Playing{}}
}

// Run keeps the gateway connected until ctx ends. When Discord refuses the
// members intent, it reconnects without it.
func (b *Bot) Run(ctx context.Context) {
	if b.cfg.Token == "" {
		b.setErr("DISCORD_TOKEN is not set in /app/data/env.sh.")
		return
	}
	if msg := checkToken(b.cfg.Token); msg != "" {
		b.setErr(msg)
		return
	}
	for ctx.Err() == nil {
		b.mu.RLock()
		members, messages := b.members, b.messages
		b.mu.RUnlock()

		fatal := make(chan error, 1)
		client, err := b.newClient(members, messages, func(_ gateway.Gateway, err error, _ bool) {
			select {
			case fatal <- err:
			default:
			}
		})
		if err == nil {
			openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = client.OpenGateway(openCtx)
			cancel()
		}
		if err != nil {
			b.log.Error("connecting to discord", "err", err)
			b.setErr("Connecting to Discord failed: " + err.Error())
			if client != nil {
				client.Close(context.Background())
			}
			sleep(ctx, 30*time.Second)
			continue
		}

		b.mu.Lock()
		b.client, b.lastErr, b.connectedAt = client, "", time.Now()
		b.mu.Unlock()
		b.log.Info("connected to discord", "members_intent", members, "message_content_intent", messages)

		select {
		case <-ctx.Done():
		case err = <-fatal:
		}
		b.mu.Lock()
		b.client = nil
		b.mu.Unlock()
		b.vmu.Lock()
		clear(b.playing)
		b.vmu.Unlock()
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		client.Close(closeCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}

		var closeErr *websocket.CloseError
		code := 0
		if errors.As(err, &closeErr) {
			code = closeErr.Code
		}
		switch code {
		case gateway.CloseEventCodeDisallowedIntent.Code:
			// drop the privileged intents one at a time, message content first
			b.mu.Lock()
			if b.messages {
				b.log.Error("discord refused the message content intent, connecting without it")
				b.messages = false
			} else {
				b.log.Error("discord refused the server members intent, connecting without it")
				b.members = false
			}
			b.mu.Unlock()
			continue
		case gateway.CloseEventCodeAuthenticationFailed.Code:
			b.setErr("Discord rejected the bot token. Set a valid DISCORD_TOKEN in /app/data/env.sh and restart the app.")
			sleep(ctx, 5*time.Minute)
		default:
			b.setErr(fmt.Sprintf("Discord closed the connection: %v", err))
			sleep(ctx, 30*time.Second)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (b *Bot) setErr(msg string) {
	b.mu.Lock()
	b.lastErr = msg
	b.mu.Unlock()
}

func (b *Bot) newClient(members, messages bool, onClose gateway.CloseHandlerFunc) (*disgobot.Client, error) {
	intents := []gateway.Intents{gateway.IntentGuilds, gateway.IntentGuildVoiceStates}
	flags := []cache.Flags{cache.FlagGuilds, cache.FlagChannels, cache.FlagRoles, cache.FlagVoiceStates, cache.FlagMembers}
	opts := []disgobot.ConfigOpt{
		disgobot.WithLogger(b.log),
		disgobot.WithCacheConfigOpts(cache.WithCaches(flags...)),
		disgobot.WithVoiceManagerConfigOpts(b.cfg.Voice...),
		disgobot.WithEventListeners(b.listeners()),
		disgobot.WithRestClientConfigOpts(rest.WithHTTPClient(&http.Client{Timeout: 20 * time.Second, Transport: &limitTransport{base: http.DefaultTransport, limits: &b.limits}})),
	}
	if messages {
		intents = append(intents, gateway.IntentGuildMessages, gateway.IntentMessageContent)
	}
	if members {
		intents = append(intents, gateway.IntentGuildMembers)
		opts = append(opts, disgobot.WithMemberChunkingFilter(disgobot.MemberChunkingFilterAll))
	}
	opts = append(opts, disgobot.WithGatewayConfigOpts(gateway.WithIntents(intents...), gateway.WithCloseHandler(onClose)))
	return disgo.New(b.cfg.Token, opts...)
}

func (b *Bot) get() (*disgobot.Client, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.client == nil {
		return nil, ErrNotConnected
	}
	return b.client, nil
}

type Status struct {
	Connected     bool
	Ready         bool
	Latency       time.Duration
	Since         time.Time
	User          string
	ApplicationID string
	MembersIntent bool
	MessageIntent bool
	Error         string
}

func (b *Bot) Status() Status {
	b.mu.RLock()
	c, st := b.client, Status{MembersIntent: b.members, MessageIntent: b.messages, Error: b.lastErr, Since: b.connectedAt}
	b.mu.RUnlock()
	if c == nil {
		return st
	}
	st.Connected = true
	st.Ready = c.Gateway != nil && c.Gateway.Status() == gateway.StatusReady
	if c.Gateway != nil {
		st.Latency = c.Gateway.Latency()
	}
	if u, ok := c.Caches.SelfUser(); ok {
		st.User = u.EffectiveName()
	}
	if c.ApplicationID != 0 {
		st.ApplicationID = c.ApplicationID.String()
	}
	return st
}

// InviteURL is empty until the bot has connected once.
func (b *Bot) InviteURL() string {
	id := b.Status().ApplicationID
	if id == "" {
		return ""
	}
	return "https://discord.com/oauth2/authorize?client_id=" + id + "&scope=bot&permissions=" + strconv.FormatInt(int64(InvitePermissions), 10)
}

// Send posts a message to a guild text channel.
func (b *Bot) Send(channelID snowflake.ID, mc discord.MessageCreate) error {
	c, err := b.get()
	if err != nil {
		return err
	}
	_, err = c.Rest.CreateMessage(channelID, mc)
	return err
}

func (b *Bot) SendDM(userID snowflake.ID, mc discord.MessageCreate) error {
	c, err := b.get()
	if err != nil {
		return err
	}
	ch, err := c.Rest.CreateDMChannel(userID)
	if err != nil {
		return err
	}
	_, err = c.Rest.CreateMessage(ch.ID(), mc)
	return err
}

// Permanent reports whether retrying cannot fix the error: missing access,
// deleted channels and users, closed DMs.
func Permanent(err error) bool {
	var re *rest.Error
	if errors.As(err, &re) {
		switch re.Code {
		case rest.JSONErrorCodeUnknownChannel, rest.JSONErrorCodeUnknownUser, rest.JSONErrorCodeUnknownMember,
			rest.JSONErrorCodeMissingAccess, rest.JSONErrorCodeLackPermissionsToPerformAction, rest.JSONErrorCodeCannotSendMessagesToThisUser:
			return true
		}
		return re.Response != nil && (re.Response.StatusCode == 403 || re.Response.StatusCode == 404)
	}
	var pe *PermanentError
	return errors.As(err, &pe)
}

// PermanentError is a problem found before calling Discord, like a missing permission.
type PermanentError struct{ Msg string }

func (e *PermanentError) Error() string { return e.Msg }

// Describe turns Discord API errors into one readable line.
func Describe(err error) string {
	var re *rest.Error
	if errors.As(err, &re) && re.Message != "" {
		return fmt.Sprintf("Discord error %d: %s", re.Code, re.Message)
	}
	return err.Error()
}

// checkToken catches values that are not a bot token, like the public key or
// the client secret of the application, which look similar in the Developer Portal.
func checkToken(token string) string {
	if strings.HasPrefix(token, "Bot ") || strings.ContainsAny(token, " \t\r\n\"'") {
		return "DISCORD_TOKEN contains spaces, quotes or a \"Bot \" prefix. Set only the token in /app/data/env.sh and restart the app."
	}
	if strings.Count(token, ".") != 2 {
		return "DISCORD_TOKEN is not a bot token. A bot token has three parts separated by dots. Copy it with Reset Token under Bot in the Discord Developer Portal, not the Public Key or Client Secret."
	}
	return ""
}
