package bot

import (
	"slices"
	"testing"

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
