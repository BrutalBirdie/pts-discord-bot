// Package commands answers the bot's slash commands and passes Discord events to the runner.
package commands

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/media"
	"github.com/BrutalBirdie/pts-discord-bot/internal/runner"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

type Handler struct {
	st  *store.Store
	bot *bot.Bot
	run *runner.Runner
	log *slog.Logger
}

func New(st *store.Store, b *bot.Bot, r *runner.Runner, log *slog.Logger) *Handler {
	return &Handler{st: st, bot: b, run: r, log: log}
}

// Discord allows 25 autocomplete choices of at most 100 characters.
const (
	maxChoices    = 25
	maxChoiceName = 100
)

func (h *Handler) Commands() []discord.ApplicationCommandCreate {
	guildOnly := []discord.InteractionContextType{discord.InteractionContextTypeGuild}
	return []discord.ApplicationCommandCreate{
		discord.SlashCommandCreate{Name: "play", Description: "Play a sound in your voice channel", Contexts: guildOnly,
			Options: []discord.ApplicationCommandOption{discord.ApplicationCommandOptionString{Name: "sound", Description: "A sound from the media library, or a random one with a tag", Required: true, Autocomplete: true}}},
		discord.SlashCommandCreate{Name: "task", Description: "Start a task of this server", Contexts: guildOnly,
			Options: []discord.ApplicationCommandOption{discord.ApplicationCommandOptionString{Name: "name", Description: "Tasks that allow /task", Required: true, Autocomplete: true}}},
		discord.SlashCommandCreate{Name: "stop", Description: "Stop the sound that is playing", Contexts: guildOnly},
		discord.SlashCommandCreate{Name: "leave", Description: "Disconnect the bot from voice", Contexts: guildOnly},
	}
}

func (h *Handler) OnEvent(ev bot.Event) { h.run.OnEvent(ev) }

func (h *Handler) audit(ev bot.Event, action, target, detail string) {
	actor := "discord:" + ev.UserName
	h.log.Info("audit", "actor", actor, "action", action, "target", target, "detail", detail)
	if err := h.st.Audit(context.Background(), store.AuditEntry{Actor: actor, Action: action, Target: target, Detail: detail}); err != nil {
		h.log.Error("audit write failed", "err", err)
	}
}

func eventOf(guildID snowflake.ID, user discord.User, member *discord.ResolvedMember) bot.Event {
	ev := bot.Event{GuildID: guildID.String(), UserID: user.ID.String(), UserName: user.EffectiveName()}
	if member != nil {
		ev.UserName = member.EffectiveName()
	}
	return ev
}

func (h *Handler) OnCommand(e *events.ApplicationCommandInteractionCreate) {
	gid := e.GuildID()
	if gid == nil {
		bot.Reply(e, "Use the commands in a server.")
		return
	}
	data := e.SlashCommandInteractionData()
	ev := eventOf(*gid, e.User(), e.Member())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	switch data.CommandName() {
	case "play":
		h.play(ctx, e, *gid, ev, data.String("sound"))
	case "task":
		h.task(ctx, e, *gid, ev, data.String("name"))
	case "stop":
		if h.bot.Stop(*gid) {
			h.audit(ev, "voice.stop", h.bot.GuildName(ev.GuildID), "")
			bot.Reply(e, "Stopped.")
		} else {
			bot.Reply(e, "Nothing is playing.")
		}
	case "leave":
		if _, ok := h.bot.InVoice(*gid); !ok {
			bot.Reply(e, "The bot is not in a voice channel.")
			return
		}
		if err := h.bot.Leave(ctx, *gid); err != nil {
			bot.Reply(e, "Could not leave: "+bot.Describe(err))
			return
		}
		h.audit(ev, "voice.leave", h.bot.GuildName(ev.GuildID), "")
		bot.Reply(e, "Left the voice channel.")
	}
}

// soundValue resolves the sound option: "id:5", "tag:greeting" or a typed name.
func (h *Handler) soundValue(ctx context.Context, value string) (tasks.Step, string, bool) {
	if tag, ok := strings.CutPrefix(value, "tag:"); ok {
		return tasks.Step{Tag: tag}, "a random sound tagged " + tag, true
	}
	if id, err := strconv.ParseInt(strings.TrimPrefix(value, "id:"), 10, 64); err == nil {
		if m, err := h.st.Media(ctx, id); err == nil && media.HasAudio(m) {
			return tasks.Step{MediaIDs: []int64{m.ID}}, m.Name, true
		}
	}
	if m, err := h.st.MediaByName(ctx, store.All, value); err == nil && media.HasAudio(m) {
		return tasks.Step{MediaIDs: []int64{m.ID}}, m.Name, true
	}
	return tasks.Step{}, "", false
}

func (h *Handler) play(ctx context.Context, e *events.ApplicationCommandInteractionCreate, gid snowflake.ID, ev bot.Event, value string) {
	step, what, ok := h.soundValue(ctx, value)
	if !ok {
		bot.Reply(e, "No sound with that name. Pick one from the suggestions.")
		return
	}
	uid, _ := bot.ParseID(ev.UserID)
	channelID, ok := h.bot.VoiceChannelOf(gid, uid)
	if !ok {
		bot.Reply(e, "Join a voice channel first, the bot plays the sound there.")
		return
	}
	// the bot only leaves again when it was not in voice before
	_, wasInVoice := h.bot.InVoice(gid)
	ch := h.bot.ChannelName(channelID.String())
	h.run.RunFromDiscord(runner.PlayTask(ev.GuildID, channelID.String(), "/play "+what, step, !wasInVoice), ev)
	h.audit(ev, "voice.play", what, h.bot.GuildName(ev.GuildID)+" · "+ch)
	bot.Reply(e, "Playing "+what+" in "+ch+".")
}

func (h *Handler) task(ctx context.Context, e *events.ApplicationCommandInteractionCreate, gid snowflake.ID, ev bot.Event, value string) {
	list, err := h.slashTasks(ctx, gid)
	if err != nil {
		h.log.Error("loading tasks for /task", "err", err)
		bot.Reply(e, "The task list could not be loaded.")
		return
	}
	for _, t := range list {
		if strconv.FormatInt(t.ID, 10) == value || strings.EqualFold(t.Name, value) {
			if problems := t.Problems(); len(problems) > 0 {
				bot.Reply(e, t.Name+" cannot run: "+strings.Join(problems, " "))
				return
			}
			h.run.RunFromDiscord(t, ev)
			h.audit(ev, "task.run", t.Name, "/task")
			bot.Reply(e, "Started "+t.Name+".")
			return
		}
	}
	bot.Reply(e, "No task with that name allows /task in this server.")
}

func (h *Handler) slashTasks(ctx context.Context, gid snowflake.ID) ([]tasks.Task, error) {
	all, err := h.st.ListTasks(ctx, store.All)
	if err != nil {
		return nil, err
	}
	var out []tasks.Task
	for _, t := range all {
		if t.Slash && t.GuildID == gid.String() {
			out = append(out, t)
		}
	}
	return out, nil
}

func choiceName(s string) string {
	if r := []rune(s); len(r) > maxChoiceName {
		return string(r[:maxChoiceName-1]) + "…"
	}
	return s
}

func (h *Handler) OnAutocomplete(e *events.AutocompleteInteractionCreate) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	focused := e.Data.Focused()
	q := strings.ToLower(strings.TrimSpace(focused.String()))
	var choices []discord.AutocompleteChoice
	add := func(name, value string) {
		if len(choices) < maxChoices && (q == "" || strings.Contains(strings.ToLower(name), q)) {
			choices = append(choices, discord.AutocompleteChoiceString{Name: choiceName(name), Value: value})
		}
	}
	gid := e.GuildID()
	switch {
	case e.Data.CommandName == "play":
		all, err := h.st.ListMedia(ctx, store.All, "")
		if err != nil {
			h.log.Error("autocomplete sounds", "err", err)
			break
		}
		slices.SortFunc(all, func(a, b store.Media) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
		for _, m := range all {
			if media.HasAudio(m) {
				add(m.Name, "id:"+strconv.FormatInt(m.ID, 10))
			}
		}
		if tags, err := h.st.Tags(ctx, store.All); err == nil {
			for _, t := range tags {
				add("Random: "+t, "tag:"+t)
			}
		}
	case e.Data.CommandName == "task" && gid != nil:
		list, err := h.slashTasks(ctx, *gid)
		if err != nil {
			h.log.Error("autocomplete tasks", "err", err)
			break
		}
		for _, t := range list {
			add(t.Name, strconv.FormatInt(t.ID, 10))
		}
	}
	if choices == nil {
		choices = []discord.AutocompleteChoice{}
	}
	if err := e.AutocompleteResult(choices); err != nil {
		h.log.Warn("answering autocomplete", "err", err)
	}
}
