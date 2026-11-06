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
	a := GuildScope(false, "A")
	if !a.Has("A") || a.Has("B") || a.Has("") {
		t.Error("guild scope")
	}
	if c := GuildScope(true, "A"); !c.Has("") || !c.Has("A") || c.Has("B") {
		t.Error("shared and guild scope")
	}
	if !All.Has("B") || !All.Has("") || All.param() != nil {
		t.Error("all")
	}
	var zero Scope
	if zero.Has("A") || zero.Has("") || zero.param() == nil {
		t.Error("the zero scope must match nothing")
	}
	discord := &Session{Provider: ProviderDiscord, Granted: []string{"A"}}
	if !discord.CanGuild("A") || discord.CanGuild("B") || discord.CanGuild("") || discord.ItemScope("B").Has("B") || discord.ItemScope("A").Has("") {
		t.Error("discord session")
	}
	cloudron := &Session{Provider: ProviderCloudron}
	if !cloudron.CanGuild("B") || !cloudron.ItemScope("A").Has("") || cloudron.ItemScope("A").Has("B") {
		t.Error("cloudron session")
	}
}

func TestRunScope(t *testing.T) {
	for _, c := range []struct {
		name, guild, updated, created string
		shared, own, other            bool
	}{
		{"saved by a Cloudron user", "A", "carol", "carol", true, true, false},
		{"edited by a Discord user", "A", "discord:9", "carol", false, true, false},
		{"created by a Discord user", "A", "discord:9", "discord:9", false, true, false},
		{"edited by a Cloudron user", "A", "carol", "discord:9", true, true, false},
		{"no updated_by, created by Discord", "A", "", "discord:9", false, true, false},
		{"no updated_by, created by Cloudron", "A", "", "carol", true, true, false},
		{"not saved, a /play or send now", "A", "", "", true, true, false},
		{"Discord user, no server", "", "discord:9", "", false, false, false},
		{"Cloudron user, no server", "", "carol", "", true, false, false},
	} {
		sc := RunScope(c.guild, c.updated, c.created)
		if sc.Has("") != c.shared || sc.Has("A") != c.own || sc.Has("B") != c.other {
			t.Errorf("%s: shared %v, own %v, other %v", c.name, sc.Has(""), sc.Has("A"), sc.Has("B"))
		}
	}
	if p := GuildScope(false, "").param(); len(p) != 0 {
		t.Errorf("an empty server id matches shared rows: %q", p)
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

func TestScopedLists(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	addMedia := func(name, guild string, tags ...string) int64 {
		t.Helper()
		id, err := s.AddMedia(ctx, Media{Name: name, Kind: KindSound, ContentType: "audio/ogg", SizeBytes: 10, UploadedBy: "t", GuildID: guild})
		must(err)
		if tags != nil {
			must(s.UpdateMedia(ctx, id, name, tags))
		}
		return id
	}
	shared := addMedia("shared.ogg", "", "fun")
	ma := addMedia("a.ogg", "A", "fun")
	mb := addMedia("b.ogg", "B", "boo")
	addMedia("dup.ogg", "A")
	dup := addMedia("dup.ogg", "")
	if _, err := s.AddMedia(ctx, Media{Name: "dup.ogg", Kind: KindSound, UploadedBy: "t", GuildID: "A"}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("name taken twice in one server: %v", err)
	}
	for _, n := range []string{"MA1", "MA2"} {
		_, err := s.CreateMessage(ctx, messages.Message{Name: n, GuildID: "A", Embeds: []messages.Embed{}}, "t")
		must(err)
	}
	msgB, err := s.CreateMessage(ctx, messages.Message{Name: "MB", GuildID: "B", Files: []int64{mb}, Embeds: []messages.Embed{}}, "t")
	must(err)
	_, err = s.CreateMessage(ctx, messages.Message{Name: "MS", Files: []int64{shared}, Embeds: []messages.Embed{}}, "t")
	must(err)
	play := func(ids ...int64) []tasks.Step { return []tasks.Step{{Type: tasks.Play, MediaIDs: ids}} }
	ta, err := s.CreateTask(ctx, tasks.Task{Name: "TA", GuildID: "A", Busy: tasks.BusyWait, Steps: play(ma)}, "t")
	must(err)
	_, err = s.CreateTask(ctx, tasks.Task{Name: "TB", GuildID: "B", Busy: tasks.BusyWait, Steps: append(play(ma), tasks.Step{Type: tasks.Message, MessageID: msgB})}, "t")
	must(err)
	for _, g := range []string{"A", "B", "B"} {
		_, err := s.StartRun(ctx, Run{Title: "run " + g, GuildID: g, Source: SourceManual})
		must(err)
	}

	a, cloudronA := GuildScope(false, "A"), GuildScope(true, "A")
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

	msgName := func(m messages.Message) string { return m.Name }
	list, err := s.FindMessages(ctx, a, "", 1, 1)
	must(err)
	if got := names(list, msgName); !slices.Equal(got, []string{"MA2"}) {
		t.Errorf("second page of server A messages: %v", got)
	}
	list, err = s.ListMessages(ctx, cloudronA)
	must(err)
	if got := names(list, msgName); !slices.Equal(got, []string{"MA1", "MA2", "MS"}) {
		t.Errorf("messages a task of A may use: %v", got)
	}
	used, others, err := s.MessageUsers(ctx, a, msgB)
	must(err)
	if len(used) != 0 || others != 1 {
		t.Errorf("message users leak to server A: %v %d", used, others)
	}

	mediaName := func(m Media) string { return m.Name + "@" + m.GuildID }
	for _, c := range []struct {
		sc   Scope
		want []string
	}{
		{a, []string{"a.ogg@A", "dup.ogg@A"}},
		{cloudronA, []string{"a.ogg@A", "dup.ogg@", "dup.ogg@A", "shared.ogg@"}},
		{All, []string{"a.ogg@A", "b.ogg@B", "dup.ogg@", "dup.ogg@A", "shared.ogg@"}},
	} {
		list, err := s.ListMedia(ctx, c.sc, "")
		must(err)
		if got := names(list, mediaName); !slices.Equal(got, c.want) {
			t.Errorf("media %+v: %v", c.sc, got)
		}
	}
	page, err := s.FindMedia(ctx, MediaQuery{Scope: a, Limit: 1, Offset: 1})
	must(err)
	if got := names(page, mediaName); !slices.Equal(got, []string{"dup.ogg@A"}) {
		t.Errorf("second page of server A media: %v", got)
	}
	list2, err := s.FindMedia(ctx, MediaQuery{Scope: a, Search: "a.ogg"})
	must(err)
	if len(list2) != 1 || !slices.Equal(list2[0].UsedBy, []string{"task TA"}) || list2[0].UsedElse != 1 {
		t.Errorf("a.ogg used by, seen from A: %+v", list2)
	}
	all, err := s.FindMedia(ctx, MediaQuery{Scope: All, Search: "a.ogg"})
	must(err)
	if len(all) != 1 || len(all[0].UsedBy) != 2 || all[0].UsedElse != 0 {
		t.Errorf("a.ogg used by, seen by Cloudron users: %+v", all)
	}
	sum, err := s.MediaSummary(ctx, a)
	must(err)
	if sum.Counts[""] != 2 || !slices.Equal(sum.Tags, []string{"fun"}) {
		t.Errorf("summary of A: %+v", sum)
	}
	hits, err := s.SearchSounds(ctx, a, "ogg", 15)
	must(err)
	if got := names(hits, mediaName); !slices.Equal(got, []string{"a.ogg@A", "dup.ogg@A"}) {
		t.Errorf("sound picker of A: %v", got)
	}
	tags, err := s.Tags(ctx, GuildScope(false, "B"))
	must(err)
	if !slices.Equal(tags, []string{"boo"}) {
		t.Errorf("tags of B: %v", tags)
	}
	if m, err := s.MediaByName(ctx, cloudronA, "dup.ogg"); err != nil || m.ID != dup {
		t.Errorf("shared file first: %+v %v", m, err)
	}
	if _, err := s.MediaByName(ctx, a, "b.ogg"); !errors.Is(err, ErrNotFound) {
		t.Errorf("file of B found from A: %v", err)
	}
	for range 10 {
		m, err := s.RandomSound(ctx, a, "fun")
		must(err)
		if m.ID != ma {
			t.Fatalf("random sound outside A: %+v", m)
		}
	}
	byDiscord, err := s.CreateTask(ctx, tasks.Task{Name: "TD", GuildID: "A", Busy: tasks.BusyWait, Steps: play(ma)}, "discord:9")
	must(err)
	must(s.SetTaskEnabled(ctx, byDiscord, false, "paused", "system"))
	td, err := s.Task(ctx, byDiscord)
	must(err)
	if td.UpdatedBy != "discord:9" {
		t.Errorf("a system pause changed updated_by: %q", td.UpdatedBy)
	}
	for range 10 {
		m, err := s.RandomSound(ctx, RunScope(td.GuildID, td.UpdatedBy, td.CreatedBy), "fun")
		must(err)
		if m.ID != ma {
			t.Fatalf("random sound of a Discord-saved task outside A: %+v", m)
		}
	}
	seen := map[int64]bool{}
	for range 50 {
		m, err := s.RandomSound(ctx, RunScope("A", "carol", "discord:9"), "fun")
		must(err)
		seen[m.ID] = true
	}
	if !seen[shared] || !seen[ma] || len(seen) != 2 {
		t.Errorf("random sounds of a Cloudron-saved task: %v", seen)
	}

	runs, err := s.ListRuns(ctx, RunQuery{Scope: GuildScope(false, "B"), Limit: 10})
	must(err)
	if n, err := s.CountRuns(ctx, RunQuery{Scope: a}); err != nil || n != 1 || len(runs) != 2 {
		t.Errorf("runs: A %d, B %d, %v", n, len(runs), err)
	}

	must(s.Trash(ctx, TrashMessage, msgB, "t"))
	must(s.Trash(ctx, TrashMedia, ma, "t"))
	must(s.Trash(ctx, TrashTask, ta, "t"))
	trash, err := s.ListTrash(ctx, a)
	must(err)
	if got := names(trash, func(it TrashItem) string { return it.Kind + " " + it.Name }); !slices.Equal(got, []string{"media a.ogg", "task TA"}) {
		t.Errorf("trash of A: %v", got)
	}
	if n, err := s.CountTrash(ctx, a); err != nil || n != 2 {
		t.Errorf("trash count of A: %d %v", n, err)
	}
	if n, err := s.CountTrash(ctx, All); err != nil || n != 3 {
		t.Errorf("trash count: %d %v", n, err)
	}
	if _, g, err := s.TrashedName(ctx, TrashMessage, msgB); err != nil || g != "B" {
		t.Errorf("owner of a trashed message: %q %v", g, err)
	}
}
