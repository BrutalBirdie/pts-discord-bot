package bot

import (
	disgobot "github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// Event is something that happened in Discord that can start tasks.
type Event struct {
	Type      string // one of tasks.EventTypes
	GuildID   string
	ChannelID string
	UserID    string
	UserName  string
	Content   string
}

// Handler receives slash commands and Discord events. The commands and runner
// packages implement it, the bot only passes things on.
type Handler interface {
	Commands() []discord.ApplicationCommandCreate
	OnCommand(e *events.ApplicationCommandInteractionCreate)
	OnAutocomplete(e *events.AutocompleteInteractionCreate)
	OnEvent(ev Event)
}

func (b *Bot) SetHandler(h Handler) { b.handler.Store(&h) }

func (b *Bot) h() Handler {
	if p := b.handler.Load(); p != nil {
		return *p
	}
	return nil
}

func (b *Bot) emit(ev Event, bot bool) {
	if h := b.h(); h != nil && !bot {
		go h.OnEvent(ev)
	}
}

func voiceEvent(typ string, guildID snowflake.ID, channelID *snowflake.ID, m discord.Member) Event {
	ev := Event{Type: typ, GuildID: guildID.String(), UserID: m.User.ID.String(), UserName: m.EffectiveName()}
	if channelID != nil {
		ev.ChannelID = channelID.String()
	}
	return ev
}

func (b *Bot) listeners() disgobot.EventListener {
	return &events.ListenerAdapter{
		OnReady: func(e *events.Ready) {
			h := b.h()
			if h == nil {
				return
			}
			go func() {
				// global commands replace the whole list, removed commands disappear
				if _, err := e.Client().Rest.SetGlobalCommands(e.Client().ApplicationID, h.Commands()); err != nil {
					b.log.Error("registering slash commands", "err", err)
				}
			}()
		},
		OnApplicationCommandInteraction: func(e *events.ApplicationCommandInteractionCreate) {
			if h := b.h(); h != nil {
				h.OnCommand(e)
			}
		},
		OnAutocompleteInteraction: func(e *events.AutocompleteInteractionCreate) {
			if h := b.h(); h != nil {
				h.OnAutocomplete(e)
			}
		},
		OnGuildVoiceJoin: func(e *events.GuildVoiceJoin) {
			b.emit(voiceEvent(tasks.VoiceJoin, e.VoiceState.GuildID, e.VoiceState.ChannelID, e.Member), e.Member.User.Bot)
		},
		OnGuildVoiceMove: func(e *events.GuildVoiceMove) {
			b.emit(voiceEvent(tasks.VoiceLeave, e.VoiceState.GuildID, e.OldVoiceState.ChannelID, e.Member), e.Member.User.Bot)
			b.emit(voiceEvent(tasks.VoiceJoin, e.VoiceState.GuildID, e.VoiceState.ChannelID, e.Member), e.Member.User.Bot)
		},
		OnGuildVoiceLeave: func(e *events.GuildVoiceLeave) {
			b.emit(voiceEvent(tasks.VoiceLeave, e.VoiceState.GuildID, e.OldVoiceState.ChannelID, e.Member), e.Member.User.Bot)
		},
		OnGuildMemberJoin: func(e *events.GuildMemberJoin) {
			b.emit(Event{Type: tasks.MemberJoin, GuildID: e.GuildID.String(), UserID: e.Member.User.ID.String(), UserName: e.Member.EffectiveName()}, e.Member.User.Bot)
		},
		OnGuildMessageCreate: func(e *events.GuildMessageCreate) {
			a := e.Message.Author
			name := a.EffectiveName()
			if e.Message.Member != nil {
				name = e.Message.Member.EffectiveName()
			}
			b.emit(Event{Type: tasks.MessageIn, GuildID: e.GuildID.String(), ChannelID: e.ChannelID.String(), UserID: a.ID.String(), UserName: name, Content: e.Message.Content}, a.Bot || a.System)
		},
	}
}

// Reply answers a slash command only the user who sent it sees.
func Reply(e *events.ApplicationCommandInteractionCreate, text string) {
	_ = e.CreateMessage(discord.MessageCreate{Content: text, Flags: discord.MessageFlagEphemeral, AllowedMentions: &discord.AllowedMentions{}})
}

// InVoice returns the voice channel the bot is in.
func (b *Bot) InVoice(guildID snowflake.ID) (snowflake.ID, bool) {
	c, err := b.get()
	if err != nil {
		return 0, false
	}
	if conn := c.VoiceManager.GetConn(guildID); conn != nil && conn.ChannelID() != nil {
		return *conn.ChannelID(), true
	}
	return 0, false
}
