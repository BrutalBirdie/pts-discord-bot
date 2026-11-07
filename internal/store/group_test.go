package store

import (
	"context"
	"slices"
	"testing"

	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
)

func TestSetGroup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	horn, err := s.AddMedia(ctx, Media{Name: "horn.ogg", Kind: KindSound, SizeBytes: 1, UploadedBy: "t"})
	must(err)
	msg, err := s.CreateMessage(ctx, messages.Message{Name: "MS", Files: []int64{horn}, Embeds: []messages.Embed{}}, "carol")
	must(err)

	must(s.SetGroup(ctx, "A", true, []int64{horn}, []int64{msg}))
	m, err := s.Media(ctx, horn)
	must(err)
	g, err := s.Message(ctx, msg)
	must(err)
	if m.GuildID != "A" || !m.GroupOnly || g.GuildID != "A" || !g.GroupOnly {
		t.Errorf("grouped: %+v %+v", m, g)
	}
	must(s.SetGroup(ctx, "", true, []int64{horn}, nil))
	if m, _ := s.Media(ctx, horn); m.GuildID != "" || m.GroupOnly {
		t.Errorf("no group keeps group only: %+v", m)
	}
	known, err := s.KnownGuilds(ctx)
	must(err)
	if !slices.Equal(known, []string{"A"}) {
		t.Errorf("known servers: %v", known)
	}
}

// Before server groups, names were unique per server: the upgrade renames clashes.
func TestUniqueNamesUpgrade(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DROP INDEX media_live_name;
CREATE UNIQUE INDEX media_owner_name_live ON media (coalesce(guild_id, ''), name) WHERE deleted_at IS NULL;
INSERT INTO media (name, kind, size_bytes, uploaded_by, guild_id) VALUES
  ('horn.ogg', 'sound', 1, 't', 'A'), ('horn.ogg', 'sound', 1, 't', NULL), ('horn.ogg', 'sound', 1, 't', 'B'),
  ('horn (2).ogg', 'sound', 1, 't', NULL), ('intro', 'sound', 1, 't', 'A'), ('intro', 'sound', 1, 't', 'B')`); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSchema(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListMedia(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range list {
		got[m.Name] = m.GuildID
	}
	want := map[string]string{"horn.ogg": "", "horn (3).ogg": "A", "horn (4).ogg": "B", "horn (2).ogg": "", "intro": "A", "intro (2)": "B"}
	if len(got) != len(want) {
		t.Fatalf("names after the upgrade: %v", got)
	}
	for name, g := range want {
		if gg, ok := got[name]; !ok || gg != g {
			t.Errorf("%s: %q %v, all %v", name, gg, ok, got)
		}
	}
	if err := s.CreateSchema(ctx); err != nil {
		t.Errorf("schema twice: %v", err)
	}
}
