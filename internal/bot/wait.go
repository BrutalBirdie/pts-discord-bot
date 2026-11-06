package bot

import (
	"context"
	"slices"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// voiceWaiters wakes the runs that wait for a voice condition in a guild.
type voiceWaiters struct {
	mu sync.Mutex
	m  map[snowflake.ID]map[chan struct{}]struct{}
}

func (w *voiceWaiters) add(guildID snowflake.ID) chan struct{} {
	ch := make(chan struct{}, 1)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.m == nil {
		w.m = map[snowflake.ID]map[chan struct{}]struct{}{}
	}
	if w.m[guildID] == nil {
		w.m[guildID] = map[chan struct{}]struct{}{}
	}
	w.m[guildID][ch] = struct{}{}
	return ch
}

func (w *voiceWaiters) remove(guildID snowflake.ID, ch chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.m[guildID], ch)
	if len(w.m[guildID]) == 0 {
		delete(w.m, guildID)
	}
}

func (w *voiceWaiters) notify(guildID snowflake.ID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for ch := range w.m[guildID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// WaitVoice returns when cond is true, checked now and after every voice state
// update in the guild, or with the error of ctx.
func (b *Bot) WaitVoice(ctx context.Context, guildID snowflake.ID, cond func() bool) error {
	if cond() {
		return nil
	}
	ch := b.waiters.add(guildID)
	defer b.waiters.remove(guildID, ch)
	for {
		// checked after add as well, an update between the first check and add is not missed
		if cond() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

// VoiceCond is a wait step condition evaluated against the voice states in
// the cache. channelID 0 means any voice channel.
func (b *Bot) VoiceCond(guildID snowflake.ID, until string, userID, channelID snowflake.ID) func() bool {
	return func() bool {
		c, err := b.get()
		if err != nil {
			return false
		}
		self := c.ID()
		isBot := func(id snowflake.ID) bool {
			if id == self {
				return true
			}
			m, ok := c.Caches.Member(guildID, id)
			return ok && m.User.Bot
		}
		return voiceCondMet(until, userID, channelID, slices.Collect(c.Caches.VoiceStates(guildID)), isBot)
	}
}

func voiceCondMet(until string, userID, channelID snowflake.ID, states []discord.VoiceState, isBot func(snowflake.ID) bool) bool {
	in := func(vs discord.VoiceState) bool {
		return vs.ChannelID != nil && (channelID == 0 || *vs.ChannelID == channelID)
	}
	var member *discord.VoiceState
	for i := range states {
		if states[i].UserID == userID && states[i].ChannelID != nil {
			member = &states[i]
		}
	}
	switch until {
	case tasks.UntilUndeafened:
		return member == nil || !member.SelfDeaf && !member.GuildDeaf
	case tasks.UntilUnmuted:
		return member == nil || !member.SelfMute && !member.GuildMute
	case tasks.UntilInVoice:
		return member != nil && in(*member)
	case tasks.UntilLeftVoice:
		return member == nil || !in(*member)
	case tasks.UntilOccupied, tasks.UntilEmpty:
		if channelID == 0 {
			return false
		}
		people := 0
		for _, vs := range states {
			if vs.ChannelID != nil && *vs.ChannelID == channelID && !isBot(vs.UserID) {
				people++
			}
		}
		return (people > 0) == (until == tasks.UntilOccupied)
	}
	return true
}
