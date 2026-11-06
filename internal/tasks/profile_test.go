package tasks

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestProfileStep(t *testing.T) {
	for name, c := range map[string]struct {
		s   Step
		err string
	}{
		"nick":         {Step{Type: Profile, Nick: "DJ {member_name}"}, ""},
		"reset nick":   {Step{Type: Profile, ResetNick: true}, ""},
		"avatar":       {Step{Type: Profile, AvatarMediaID: 4}, ""},
		"both":         {Step{Type: Profile, Nick: "DJ", ResetAvatar: true}, ""},
		"nothing":      {Step{Type: Profile}, "pick a nickname or an avatar to change"},
		"set and drop": {Step{Type: Profile, Nick: "DJ", ResetNick: true}, "set or reset, not both"},
		"avatar twice": {Step{Type: Profile, AvatarMediaID: 4, ResetAvatar: true}, "set or reset, not both"},
		"long nick":    {Step{Type: Profile, Nick: strings.Repeat("ä", MaxNick+1)}, "the nickname is longer than 32 characters"},
		"32 runes":     {Step{Type: Profile, Nick: strings.Repeat("ä", MaxNick)}, ""},
	} {
		got := ""
		if err := c.s.Validate(); err != nil {
			got = err.Error()
		}
		if got != c.err {
			t.Errorf("%s: got %q, want %q", name, got, c.err)
		}
	}
	s := Step{Type: Profile, Nick: "DJ", AvatarMediaID: 4}
	if got := s.ProfileText("cat.png"); got != "set nickname to DJ and set avatar to cat.png" {
		t.Errorf("text %q", got)
	}
	if got := (Step{Type: Profile, ResetNick: true, ResetAvatar: true}).ProfileText(""); got != "reset nickname and reset avatar" {
		t.Errorf("text %q", got)
	}
	if got := s.ProfileFields(); !slices.Equal(got, []string{FieldNick, FieldAvatar}) {
		t.Errorf("fields %v", got)
	}

	var back Step
	b, _ := json.Marshal(s)
	if string(b) != `{"type":"profile","nick":"DJ","avatar_media_id":4}` {
		t.Errorf("saved as %s", b)
	}
	if err := json.Unmarshal(b, &back); err != nil || back.AvatarMediaID != 4 || len(back.MediaIDs) != 0 {
		t.Errorf("loaded %+v %v", back, err)
	}
}

func TestProfileDecide(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	nick, avatar := []string{FieldNick}, []string{FieldAvatar}
	for name, c := range map[string]struct {
		limits map[string]ProfileLimit
		fields []string
		wait   time.Duration
		skip   bool
	}{
		"never changed":         {nil, nick, 0, false},
		"nick long ago":         {map[string]ProfileLimit{FieldNick: {ChangedAt: now.Add(-time.Hour)}}, nick, 0, false},
		"nick just now":         {map[string]ProfileLimit{FieldNick: {ChangedAt: now.Add(-2 * time.Second)}}, nick, 3 * time.Second, false},
		"avatar just now":       {map[string]ProfileLimit{FieldAvatar: {ChangedAt: now.Add(-10 * time.Second)}}, avatar, 50 * time.Second, true},
		"avatar 40s ago":        {map[string]ProfileLimit{FieldAvatar: {ChangedAt: now.Add(-40 * time.Second)}}, avatar, 20 * time.Second, false},
		"other field limited":   {map[string]ProfileLimit{FieldAvatar: {ChangedAt: now}}, nick, 0, false},
		"short 429":             {map[string]ProfileLimit{FieldNick: {BlockedUntil: now.Add(30 * time.Second)}}, nick, 30 * time.Second, false},
		"long 429":              {map[string]ProfileLimit{FieldNick: {BlockedUntil: now.Add(10 * time.Minute)}}, nick, 10 * time.Minute, true},
		"429 over":              {map[string]ProfileLimit{FieldNick: {BlockedUntil: now.Add(-time.Second)}}, nick, 0, false},
		"both, the later wins":  {map[string]ProfileLimit{FieldNick: {ChangedAt: now.Add(-time.Second)}, FieldAvatar: {BlockedUntil: now.Add(20 * time.Second)}}, []string{FieldNick, FieldAvatar}, 20 * time.Second, false},
		"429 later than change": {map[string]ProfileLimit{FieldAvatar: {ChangedAt: now.Add(-time.Hour), BlockedUntil: now.Add(time.Hour)}}, avatar, time.Hour, true},
	} {
		wait, at, skip := ProfileDecide(now, c.limits, c.fields)
		if wait != c.wait || skip != c.skip || !at.Equal(now.Add(c.wait)) {
			t.Errorf("%s: got %v %v %v, want %v %v", name, wait, at, skip, c.wait, c.skip)
		}
	}
}
