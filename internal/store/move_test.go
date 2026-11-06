package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

func TestMoveItems(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	add := func(name, guild string) int64 {
		t.Helper()
		id, err := s.AddMedia(ctx, Media{Name: name, Kind: KindSound, SizeBytes: 1, UploadedBy: "t", GuildID: guild})
		must(err)
		return id
	}
	horn, clash := add("horn.ogg", ""), add("clash.ogg", "")
	add("clash.ogg", "A")
	msg, err := s.CreateMessage(ctx, messages.Message{Name: "MS", Files: []int64{horn}, Embeds: []messages.Embed{}}, "carol")
	must(err)
	play := []tasks.Step{{Type: tasks.Play, MediaIDs: []int64{horn}}, {Type: tasks.Message, MessageID: msg}}
	_, err = s.CreateTask(ctx, tasks.Task{Name: "TA", GuildID: "A", Busy: tasks.BusyWait, Steps: play}, "carol")
	must(err)
	_, err = s.CreateTask(ctx, tasks.Task{Name: "TB", GuildID: "B", Busy: tasks.BusyWait, Steps: play}, "carol")
	must(err)

	uses, err := s.MediaUses(ctx, []int64{horn, clash})
	must(err)
	breaks := func(list []Use, guild string) []string {
		var out []string
		for _, u := range list {
			if u.Breaks(guild) {
				out = append(out, u.Kind+" "+u.Name)
			}
		}
		return out
	}
	if got := breaks(uses[horn], "A"); !slices.Equal(got, []string{"task TB", "message MS"}) {
		t.Errorf("users of horn.ogg failing in A: %v", got)
	}
	if got := breaks(uses[horn], ""); len(got) != 0 || len(uses[horn]) != 3 || len(uses[clash]) != 0 {
		t.Errorf("uses: %+v", uses)
	}
	mu, err := s.MessageUses(ctx, msg)
	must(err)
	if got := breaks(mu, "B"); !slices.Equal(got, []string{"task TA"}) {
		t.Errorf("users of MS failing in B: %v", got)
	}

	err = s.MoveItems(ctx, "A", []int64{horn, clash}, []int64{msg})
	var taken *NameTakenError
	if !errors.As(err, &taken) || taken.Name != "clash.ogg" || !errors.Is(err, ErrNameTaken) {
		t.Fatalf("name clash: %v", err)
	}
	if m, _ := s.Media(ctx, horn); m.GuildID != "" {
		t.Errorf("a refused move moved horn.ogg to %q", m.GuildID)
	}
	must(s.MoveItems(ctx, "A", []int64{horn}, []int64{msg}))
	m, err := s.Media(ctx, horn)
	must(err)
	g, err := s.Message(ctx, msg)
	must(err)
	if m.GuildID != "A" || g.GuildID != "A" {
		t.Errorf("moved to %q and %q", m.GuildID, g.GuildID)
	}
	must(s.MoveItems(ctx, "", []int64{horn}, nil))
	if m, _ := s.Media(ctx, horn); m.GuildID != "" {
		t.Errorf("back to shared: %q", m.GuildID)
	}
	known, err := s.KnownGuilds(ctx)
	must(err)
	if !slices.Equal(known, []string{"A", "B"}) {
		t.Errorf("known servers: %v", known)
	}
}
