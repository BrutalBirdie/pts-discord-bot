package bot

import "testing"

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
