package store

import (
	"context"
	"slices"
	"testing"
)

func TestLookFavoritesAndPreviewVolume(t *testing.T) {
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
	pic, err := s.AddMedia(ctx, Media{Name: "face.png", Kind: KindImage, SizeBytes: 1, UploadedBy: "t"})
	must(err)

	must(s.EditSound(ctx, SoundEdit{ID: horn, Name: "horn.ogg", Emoji: "🎺", IconID: pic, Volume: 80, TrimStartMS: 500, TrimEndMS: 1500}))
	if m, _ := s.Media(ctx, horn); m.Emoji != "🎺" || m.IconID != pic || m.Volume != 80 || m.PlayMS() != 1000 {
		t.Errorf("edit: %+v", m)
	}
	must(s.EditSound(ctx, SoundEdit{ID: horn, Name: "horn.ogg", DisplayName: "Big Horn", Volume: 80, TrimStartMS: 500, TrimEndMS: 1500}))
	if hits, _ := s.SearchSounds(ctx, "", "big h", 5); len(hits) != 1 || hits[0].Label() != "Big Horn" {
		t.Errorf("search by display name: %v", hits)
	}
	if hits, _ := s.FindMedia(ctx, MediaQuery{Scope: All, Search: "big"}); len(hits) != 1 {
		t.Errorf("media search by display name: %v", hits)
	}
	horn2, err := s.AddMedia(ctx, Media{Name: "horn2.ogg", Kind: KindSound, SizeBytes: 1, UploadedBy: "t"})
	must(err)
	must(s.UpdateMedia(ctx, Media{ID: horn, Name: "horn.ogg", DisplayName: "Big Horn", Tags: []string{"fun", "loud"}}))
	must(s.UpdateMedia(ctx, Media{ID: horn2, Name: "horn2.ogg", Tags: []string{"fun"}}))
	must(s.SetTagLook(ctx, "fun", "🎉", 0))
	tags, err := s.SoundTags(ctx, "")
	must(err)
	if len(tags) != 2 || tags[0] != (SoundTag{Name: "fun", Count: 2, Emoji: "🎉"}) || tags[1] != (SoundTag{Name: "loud", Count: 1}) {
		t.Errorf("tags: %+v", tags)
	}
	must(s.SetTagLook(ctx, "fun", "", 0))
	if tags, _ := s.SoundTags(ctx, ""); tags[0].Emoji != "" {
		t.Errorf("tag look not removed: %+v", tags)
	}
	if (Media{Name: "x.ogg"}).Label() != "x.ogg" {
		t.Error("label without a display name")
	}
	if err := s.EditSound(ctx, SoundEdit{ID: horn, Name: "face.png"}); err != ErrNameTaken {
		t.Errorf("taken name: %v", err)
	}
	if err := s.EditSound(ctx, SoundEdit{ID: 999, Name: "x"}); err != ErrNotFound {
		t.Errorf("edit of a missing file: %v", err)
	}
	if (Media{DurationMS: 3000, TrimStartMS: 1000}).PlayMS() != 2000 || (Media{DurationMS: 3000, TrimEndMS: 5000}).PlayMS() != 3000 {
		t.Error("PlayMS")
	}

	must(s.SetFavorite(ctx, "ann", horn, true))
	must(s.SetFavorite(ctx, "ann", horn, true))
	favs, err := s.Favorites(ctx, "ann")
	must(err)
	if !favs[horn] || len(favs) != 1 {
		t.Errorf("favorites: %v", favs)
	}
	if err := s.SetFavorite(ctx, "ann", 999, true); err != ErrNotFound {
		t.Errorf("favorite of a missing file: %v", err)
	}
	must(s.Trash(ctx, TrashMedia, horn, "ann"))
	if list, _ := s.FavoriteSounds(ctx, "ann"); len(list) != 0 {
		t.Errorf("trashed favorite listed: %v", list)
	}
	must(s.PurgeTrashed(ctx, TrashMedia, horn))
	if favs, _ := s.Favorites(ctx, "ann"); len(favs) != 0 {
		t.Errorf("purged favorite kept: %v", favs)
	}

	must(s.SetPrefs(ctx, "ann", Prefs{ShowLabels: true}))
	must(s.SetPreviewVolume(ctx, "ann", 35))
	must(s.SetPlayVolume(ctx, "ann", 150))
	must(s.SetGuildOrder(ctx, "ann", []string{"2", "1"}))
	must(s.SetPrefs(ctx, "ann", Prefs{ShowLabels: true}))
	if p, _ := s.Prefs(ctx, "ann"); p.PreviewVolume != 35 || p.PlayVolume != 150 || !slices.Equal(p.GuildOrder, []string{"2", "1"}) || !p.ShowLabels {
		t.Errorf("prefs: %+v", p)
	}
	if p, _ := s.Prefs(ctx, "bob"); p.PreviewVolume != 100 || p.PlayVolume != 100 {
		t.Errorf("default preview volume: %+v", p)
	}
}
