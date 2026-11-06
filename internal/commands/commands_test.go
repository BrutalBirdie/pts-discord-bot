package commands

import (
	"context"
	"slices"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// fakeStore applies scopes like the SQL queries do.
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

func (f *fakeStore) MediaByName(_ context.Context, sc store.Scope, name string) (store.Media, error) {
	for _, m := range f.media {
		if m.Name == name && sc.Has(m.GuildID) {
			return m, nil
		}
	}
	return store.Media{}, store.ErrNotFound
}

func (f *fakeStore) ListMedia(_ context.Context, sc store.Scope, _ string) ([]store.Media, error) {
	var out []store.Media
	for _, m := range f.media {
		if sc.Has(m.GuildID) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeStore) Tags(_ context.Context, sc store.Scope) ([]string, error) {
	var out []string
	for _, m := range f.media {
		if sc.Has(m.GuildID) {
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
			{ID: 3, Name: "b.ogg", Kind: store.KindSound, GuildID: guildB.String(), Tags: []string{"secret"}},
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
		{"play in A", "play", &a, "", []string{"a.ogg", "shared.ogg", "Random: fun", "Random: loud"}},
		{"play in B", "play", &b, "", []string{"b.ogg", "shared.ogg", "Random: fun", "Random: secret"}},
		{"play in A, other server typed", "play", &a, "b.o", []string{}},
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
		gid   snowflake.ID
		want  []int64
	}{
		{"id:1", guildA, []int64{1}},
		{"id:2", guildA, []int64{2}},
		{"id:3", guildA, nil},
		{"id:3", guildB, []int64{3}},
		{"b.ogg", guildA, nil},
		{"a.ogg", guildA, []int64{2}},
		{"id:4", guildA, nil},
	} {
		step, _, ok := h.soundValue(context.Background(), c.gid, c.value)
		if ok != (c.want != nil) || !slices.Equal(step.MediaIDs, c.want) {
			t.Errorf("%s in %s: %v %v", c.value, c.gid, ok, step.MediaIDs)
		}
	}
	if step, _, ok := h.soundValue(context.Background(), guildA, "tag:secret"); !ok || step.Tag != "secret" {
		t.Errorf("tag value: %+v", step)
	}
}
