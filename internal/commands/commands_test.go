package commands

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// fakeStore offers media like the SQL queries do.
type fakeStore struct {
	media []store.Media
	tasks []tasks.Task
}

func (f *fakeStore) Media(_ context.Context, id int64) (store.Media, error) {
	for _, m := range f.media {
		if m.ID == id {
			return m, nil
		}
	}
	return store.Media{}, store.ErrNotFound
}

func (f *fakeStore) MediaByName(_ context.Context, name string) (store.Media, error) {
	for _, m := range f.media {
		if m.Name == name {
			return m, nil
		}
	}
	return store.Media{}, store.ErrNotFound
}

func offered(m store.Media, guildID string) bool { return !m.GroupOnly || m.GuildID == guildID }

func (f *fakeStore) SearchSounds(_ context.Context, guildID, q string, limit int) ([]store.Media, error) {
	var own, rest []store.Media
	for _, m := range f.media {
		if m.Kind != store.KindSound || !(offered(m, guildID) && strings.Contains(m.Name, q) || q != "" && m.Name == q) {
			continue
		}
		if m.GuildID == guildID {
			own = append(own, m)
		} else {
			rest = append(rest, m)
		}
	}
	out := append(own, rest...)
	return out[:min(len(out), limit)], nil
}

func (f *fakeStore) Tags(_ context.Context, guildID string) ([]string, error) {
	var out []string
	for _, m := range f.media {
		if offered(m, guildID) {
			out = append(out, m.Tags...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

func (f *fakeStore) ListTasks(_ context.Context, sc store.Scope) ([]tasks.Task, error) {
	var out []tasks.Task
	for _, t := range f.tasks {
		if sc.Has(t.GuildID) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeStore) Audit(context.Context, store.AuditEntry) error { return nil }

const guildA, guildB snowflake.ID = 100, 200

func testHandler() *Handler {
	return &Handler{st: &fakeStore{
		media: []store.Media{
			{ID: 1, Name: "shared.ogg", Kind: store.KindSound, Tags: []string{"fun"}},
			{ID: 2, Name: "a.ogg", Kind: store.KindSound, GuildID: guildA.String(), Tags: []string{"loud"}},
			{ID: 3, Name: "b.ogg", Kind: store.KindSound, GuildID: guildB.String(), GroupOnly: true, Tags: []string{"secret"}},
			{ID: 5, Name: "open-b.ogg", Kind: store.KindSound, GuildID: guildB.String()},
			{ID: 4, Name: "a.png", Kind: store.KindImage, GuildID: guildA.String()},
		},
		tasks: []tasks.Task{
			{ID: 10, Name: "Task A", GuildID: guildA.String(), Slash: true},
			{ID: 11, Name: "Quiet A", GuildID: guildA.String()},
			{ID: 12, Name: "Task B", GuildID: guildB.String(), Slash: true},
		},
	}}
}

func choiceNames(list []discord.AutocompleteChoice) []string {
	out := []string{}
	for _, c := range list {
		out = append(out, c.ChoiceName())
	}
	return out
}

func TestAutocomplete(t *testing.T) {
	h := testHandler()
	a, b := guildA, guildB
	for _, c := range []struct {
		name, command string
		gid           *snowflake.ID
		typed         string
		want          []string
	}{
		{"play in A", "play", &a, "", []string{"a.ogg", "shared.ogg", "open-b.ogg", "Random: fun", "Random: loud"}},
		{"play in B", "play", &b, "", []string{"b.ogg", "open-b.ogg", "shared.ogg", "a.ogg", "Random: fun", "Random: loud", "Random: secret"}},
		{"play in A, part of a name B keeps to itself", "play", &a, "b.o", []string{"open-b.ogg"}},
		{"play in A, exact name B keeps to itself", "play", &a, "b.ogg", []string{"b.ogg", "open-b.ogg"}},
		{"task in A", "task", &a, "", []string{"Task A"}},
		{"task in B", "task", &b, "", []string{"Task B"}},
		{"no server", "play", nil, "", []string{}},
	} {
		if got := choiceNames(h.choices(context.Background(), c.command, c.gid, c.typed)); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

func TestSoundValue(t *testing.T) {
	h := testHandler()
	for _, c := range []struct {
		value string
		want  []int64
	}{
		{"id:1", []int64{1}},
		{"id:3", []int64{3}},
		{"b.ogg", []int64{3}},
		{"nope.ogg", nil},
		{"id:4", nil},
	} {
		step, _, ok := h.soundValue(context.Background(), c.value)
		if ok != (c.want != nil) || !slices.Equal(step.MediaIDs, c.want) {
			t.Errorf("%s: %v %v", c.value, ok, step.MediaIDs)
		}
	}
	if step, _, ok := h.soundValue(context.Background(), "tag:secret"); !ok || step.Tag != "secret" {
		t.Errorf("tag value: %+v", step)
	}
}
