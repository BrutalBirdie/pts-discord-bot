package bot

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

func TestCheckToken(t *testing.T) {
	for token, ok := range map[string]bool{
		"first.second.third": true,
		"4fc9a0d1e2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d": false,
		"Bot MTIz.Ga.abc": false,
	} {
		if got := checkToken(token) == ""; got != ok {
			t.Errorf("%q: valid=%v, want %v", token, got, ok)
		}
	}
}

func TestMatchMember(t *testing.T) {
	members := []Member{
		{ID: "111111111111111111", Name: "Jane", Username: "jane.doe"},
		{ID: "222222222222222222", Name: "Amira K.", Username: "amira"},
		{ID: "333333333333333333", Name: "Amira K.", Username: "amira2"},
		{ID: "444444444444444444", Name: "YT-BOT", Username: "ytbot", Bot: true},
	}
	for in, want := range map[string]string{
		"111111111111111111":     "111111111111111111",
		"<@!222222222222222222>": "222222222222222222",
		"Jane (@jane.doe)":       "111111111111111111",
		"@AMIRA":                 "222222222222222222",
		"jane":                   "111111111111111111",
		"999999999999999999":     "999999999999999999",
		"Amira K.":               "",
		"nobody":                 "",
		"Amira K. (@amira2)":     "333333333333333333",
	} {
		m, ok := MatchMember(members, in)
		if got := m.ID; !ok && got != "" || ok && got != want || !ok && want != "" {
			t.Errorf("%q: got %q %v, want %q", in, got, ok, want)
		}
	}
	got := SearchMembers(members, "am", 10)
	if len(got) != 2 || got[0].Username != "amira" {
		t.Errorf("search: %+v", got)
	}
	if got := SearchMembers(members, "ne", 10); len(got) != 1 || got[0].Username != "jane.doe" {
		t.Errorf("contains search: %+v", got)
	}
	if got := SearchMembers(members, "yt", 10); len(got) != 0 {
		t.Errorf("bots must be left out: %+v", got)
	}
}

func TestVoiceTransitions(t *testing.T) {
	a, b := snowflake.ID(1), snowflake.ID(2)
	m := discord.Member{User: discord.User{ID: 7, Username: "jane"}}
	for name, c := range map[string]struct {
		old, cur *snowflake.ID
		want     []string
	}{
		"join":         {nil, &a, []string{tasks.VoiceJoin + " 1"}},
		"leave":        {&a, nil, []string{tasks.VoiceLeave + " 1"}},
		"move":         {&a, &b, []string{tasks.VoiceLeave + " 1", tasks.VoiceJoin + " 2"}},
		"mute or deaf": {&a, &a, nil},
		"not in voice": {nil, nil, nil},
	} {
		var got []string
		for _, ev := range voiceTransitions(9, c.old, c.cur, m) {
			got = append(got, ev.Type+" "+ev.ChannelID)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}

func TestVoiceCondMet(t *testing.T) {
	lobby, other := snowflake.ID(2), snowflake.ID(3)
	jane, botID := snowflake.ID(7), snowflake.ID(8)
	isBot := func(id snowflake.ID) bool { return id == botID }
	in := func(user, ch snowflake.ID, edit func(*discord.VoiceState)) []discord.VoiceState {
		vs := discord.VoiceState{GuildID: 1, UserID: user, ChannelID: &ch}
		if edit != nil {
			edit(&vs)
		}
		return []discord.VoiceState{vs}
	}
	deaf := in(jane, lobby, func(vs *discord.VoiceState) { vs.SelfDeaf = true })
	guildMuted := in(jane, lobby, func(vs *discord.VoiceState) { vs.GuildMute = true })
	inLobby := in(jane, lobby, nil)
	onlyBot := in(botID, lobby, nil)
	for name, c := range map[string]struct {
		until   string
		channel snowflake.ID
		states  []discord.VoiceState
		want    bool
	}{
		"deafened":              {tasks.UntilUndeafened, 0, deaf, false},
		"undeafened":            {tasks.UntilUndeafened, 0, inLobby, true},
		"guild deafened":        {tasks.UntilUndeafened, 0, in(jane, lobby, func(vs *discord.VoiceState) { vs.GuildDeaf = true }), false},
		"not in voice is heard": {tasks.UntilUndeafened, 0, nil, true},
		"guild muted":           {tasks.UntilUnmuted, 0, guildMuted, false},
		"unmuted":               {tasks.UntilUnmuted, 0, deaf, true},
		"in any voice":          {tasks.UntilInVoice, 0, inLobby, true},
		"in another channel":    {tasks.UntilInVoice, other, inLobby, false},
		"in the channel":        {tasks.UntilInVoice, lobby, inLobby, true},
		"not in voice":          {tasks.UntilInVoice, 0, nil, false},
		"still in voice":        {tasks.UntilLeftVoice, 0, inLobby, false},
		"left the channel":      {tasks.UntilLeftVoice, other, inLobby, true},
		"left voice":            {tasks.UntilLeftVoice, 0, onlyBot, true},
		"occupied":              {tasks.UntilOccupied, lobby, inLobby, true},
		"only the bot":          {tasks.UntilOccupied, lobby, onlyBot, false},
		"empty with bot":        {tasks.UntilEmpty, lobby, onlyBot, true},
		"not empty":             {tasks.UntilEmpty, lobby, inLobby, false},
		"other channel empty":   {tasks.UntilEmpty, other, inLobby, true},
		"no channel":            {tasks.UntilEmpty, 0, nil, false},
	} {
		if got := voiceCondMet(c.until, jane, c.channel, c.states, isBot); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}

func TestWaitVoice(t *testing.T) {
	b := &Bot{}
	var ready atomic.Bool
	checks := atomic.Int32{}
	cond := func() bool { checks.Add(1); return ready.Load() }
	done := make(chan error, 1)
	go func() { done <- b.WaitVoice(context.Background(), 1, cond) }()
	for checks.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	b.waiters.notify(2)
	b.waiters.notify(1)
	select {
	case err := <-done:
		t.Fatalf("returned while cond is false: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	ready.Store(true)
	b.waiters.notify(1)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("not woken by the update")
	}
	if len(b.waiters.m) != 0 {
		t.Errorf("waiter left behind: %v", b.waiters.m)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := b.WaitVoice(ctx, 1, func() bool { return false }); err != context.DeadlineExceeded {
		t.Errorf("timeout: %v", err)
	}
	if err := b.WaitVoice(context.Background(), 1, func() bool { return true }); err != nil {
		t.Errorf("already true: %v", err)
	}
}
