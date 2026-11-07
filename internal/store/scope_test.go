package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

func TestScope(t *testing.T) {
	a := GuildScope("A")
	if !a.Has("A") || a.Has("B") || a.Has("") {
		t.Error("guild scope")
	}
	if !All.Has("B") || !All.Has("") || All.param() != nil {
		t.Error("all")
	}
	var zero Scope
	if zero.Has("A") || zero.Has("") || zero.param() == nil {
		t.Error("the zero scope must match nothing")
	}
	if p := GuildScope("").param(); len(p) != 0 {
		t.Errorf("an empty server id is in scope: %q", p)
	}
	discord := &Session{Provider: ProviderDiscord, Granted: []string{"A"}}
	if !discord.CanGuild("A") || discord.CanGuild("B") || discord.CanGuild("") {
		t.Error("discord session")
	}
	cloudron := &Session{Provider: ProviderCloudron}
	if !cloudron.CanGuild("B") || !cloudron.Scope().Has("B") {
		t.Error("cloudron session")
	}
}

func names[T any](list []T, name func(T) string) []string {
	out := []string{}
	for _, x := range list {
		out = append(out, name(x))
	}
	slices.Sort(out)
	return out
}

func TestOpenItems(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	addMedia := func(name, guild string, only bool, tags ...string) int64 {
		t.Helper()
		id, err := s.AddMedia(ctx, Media{Name: name, Kind: KindSound, ContentType: "audio/ogg", SizeBytes: 10, UploadedBy: "t", GuildID: guild, GroupOnly: only})
		must(err)
		if tags != nil {
			must(s.UpdateMedia(ctx, Media{ID: id, Name: name, Tags: tags, GuildID: guild, GroupOnly: only}))
		}
		return id
	}
	none := addMedia("none.ogg", "", true, "fun")
	ma := addMedia("a.ogg", "A", false, "fun")
	mb := addMedia("b.ogg", "B", true, "fun", "boo")
	if _, err := s.AddMedia(ctx, Media{Name: "a.ogg", Kind: KindSound, UploadedBy: "t", GuildID: "B"}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("a name taken in another server: %v", err)
	}
	if m, err := s.Media(ctx, none); err != nil || m.GroupOnly {
		t.Errorf("group only without a group: %+v %v", m, err)
	}
	for _, c := range []struct {
		name, guild string
		only        bool
	}{{"MA1", "A", false}, {"MA2", "A", false}, {"MB", "B", true}, {"MN", "", false}} {
		_, err := s.CreateMessage(ctx, messages.Message{Name: c.name, GuildID: c.guild, GroupOnly: c.only, Embeds: []messages.Embed{}}, "t")
		must(err)
	}
	msgs, err := s.ListMessages(ctx, "")
	must(err)
	var msgB int64
	for _, m := range msgs {
		if m.Name == "MB" {
			msgB = m.ID
		}
	}
	play := func(ids ...int64) []tasks.Step { return []tasks.Step{{Type: tasks.Play, MediaIDs: ids}} }
	ta, err := s.CreateTask(ctx, tasks.Task{Name: "TA", GuildID: "A", Busy: tasks.BusyWait, Steps: play(mb)}, "t")
	must(err)
	_, err = s.CreateTask(ctx, tasks.Task{Name: "TB", GuildID: "B", Busy: tasks.BusyWait, Steps: append(play(ma), tasks.Step{Type: tasks.Message, MessageID: msgB})}, "t")
	must(err)
	for _, g := range []string{"A", "B", "B"} {
		_, err := s.StartRun(ctx, Run{Title: "run " + g, GuildID: g, Source: SourceManual})
		must(err)
	}

	a := GuildScope("A")
	taskName := func(x tasks.Task) string { return x.Name }
	for _, c := range []struct {
		sc   Scope
		want []string
	}{{a, []string{"TA"}}, {All, []string{"TA", "TB"}}, {Scope{}, []string{}}} {
		list, err := s.ListTasks(ctx, c.sc)
		must(err)
		if got := names(list, taskName); !slices.Equal(got, c.want) {
			t.Errorf("tasks %+v: %v", c.sc, got)
		}
	}

	ordered := func(list []messages.Message) []string {
		out := []string{}
		for _, m := range list {
			out = append(out, m.Name)
		}
		return out
	}
	for _, c := range []struct {
		q    MessageQuery
		want []string
	}{
		{MessageQuery{}, []string{"MA1", "MA2", "MB", "MN"}},
		{MessageQuery{Offer: "A"}, []string{"MA1", "MA2", "MN"}},
		{MessageQuery{Offer: "B"}, []string{"MB", "MA1", "MA2", "MN"}},
		{MessageQuery{Group: "A"}, []string{"MA1", "MA2"}},
		{MessageQuery{Group: NoGroup}, []string{"MN"}},
		{MessageQuery{Limit: 1, Offset: 1}, []string{"MA2"}},
	} {
		list, err := s.FindMessages(ctx, c.q)
		must(err)
		if got := ordered(list); !slices.Equal(got, c.want) {
			t.Errorf("messages %+v: %v", c.q, got)
		}
	}
	used, others, err := s.MessageUsers(ctx, a, msgB)
	must(err)
	if len(used) != 0 || others != 1 {
		t.Errorf("message users leak to server A: %v %d", used, others)
	}

	mediaNames := func(list []Media) []string {
		out := []string{}
		for _, m := range list {
			out = append(out, m.Name)
		}
		return out
	}
	for _, c := range []struct {
		guild string
		want  []string
	}{
		{"", []string{"a.ogg", "b.ogg", "none.ogg"}},
		{"A", []string{"a.ogg", "none.ogg"}},
		{"B", []string{"b.ogg", "a.ogg", "none.ogg"}},
	} {
		list, err := s.ListMedia(ctx, c.guild, "")
		must(err)
		if got := mediaNames(list); !slices.Equal(got, c.want) {
			t.Errorf("media offered to %q: %v", c.guild, got)
		}
	}
	page, err := s.FindMedia(ctx, MediaQuery{Scope: a, Limit: 1, Offset: 1})
	must(err)
	if got := mediaNames(page); !slices.Equal(got, []string{"b.ogg"}) {
		t.Errorf("second page of media: %v", got)
	}
	if list, err := s.FindMedia(ctx, MediaQuery{Scope: All, Group: NoGroup}); err != nil || !slices.Equal(mediaNames(list), []string{"none.ogg"}) {
		t.Errorf("media without a group: %v %v", mediaNames(list), err)
	}
	list2, err := s.FindMedia(ctx, MediaQuery{Scope: a, Search: "a.ogg"})
	must(err)
	if len(list2) != 1 || len(list2[0].UsedBy) != 0 || list2[0].UsedElse != 1 {
		t.Errorf("a.ogg used by, seen from A: %+v", list2)
	}
	all, err := s.FindMedia(ctx, MediaQuery{Scope: All, Search: "b.ogg"})
	must(err)
	if len(all) != 1 || !slices.Equal(all[0].UsedBy, []string{"task TA"}) || all[0].UsedElse != 0 {
		t.Errorf("b.ogg used by, seen by Cloudron users: %+v", all)
	}
	sum, err := s.MediaSummary(ctx)
	must(err)
	if sum.Counts[""] != 3 || !slices.Equal(sum.Tags, []string{"boo", "fun"}) {
		t.Errorf("summary: %+v", sum)
	}
	hits, err := s.SearchSounds(ctx, "A", "ogg", 15)
	must(err)
	if got := mediaNames(hits); !slices.Equal(got, []string{"a.ogg", "none.ogg"}) {
		t.Errorf("sound picker of A: %v", got)
	}
	hits, err = s.SearchSounds(ctx, "A", "B.ogg", 15)
	must(err)
	if got := mediaNames(hits); !slices.Equal(got, []string{"b.ogg"}) {
		t.Errorf("exact name of a file B keeps to itself, from A: %v", got)
	}
	tags, err := s.Tags(ctx, "A")
	must(err)
	if !slices.Equal(tags, []string{"fun"}) {
		t.Errorf("tags offered to A: %v", tags)
	}
	if m, err := s.MediaByName(ctx, "b.ogg"); err != nil || m.ID != mb {
		t.Errorf("file by name: %+v %v", m, err)
	}
	seen := map[int64]bool{}
	for range 50 {
		m, err := s.RandomSound(ctx, "A", "fun")
		must(err)
		seen[m.ID] = true
	}
	if !seen[none] || !seen[ma] || len(seen) != 2 {
		t.Errorf("random sounds for A: %v", seen)
	}

	runs, err := s.ListRuns(ctx, RunQuery{Scope: GuildScope("B"), Limit: 10})
	must(err)
	if n, err := s.CountRuns(ctx, RunQuery{Scope: a}); err != nil || n != 1 || len(runs) != 2 {
		t.Errorf("runs: A %d, B %d, %v", n, len(runs), err)
	}

	must(s.Trash(ctx, TrashMessage, msgB, "t"))
	must(s.Trash(ctx, TrashMedia, ma, "t"))
	must(s.Trash(ctx, TrashTask, ta, "t"))
	trash, err := s.ListTrash(ctx, GuildScope("B"))
	must(err)
	if got := names(trash, func(it TrashItem) string { return it.Kind + " " + it.Name }); !slices.Equal(got, []string{"media a.ogg", "message MB"}) {
		t.Errorf("trash seen from B: %v", got)
	}
	if n, err := s.CountTrash(ctx, a); err != nil || n != 3 {
		t.Errorf("trash count of A: %d %v", n, err)
	}
	if _, g, err := s.TrashedName(ctx, TrashMessage, msgB); err != nil || g != "B" {
		t.Errorf("group of a trashed message: %q %v", g, err)
	}
}
