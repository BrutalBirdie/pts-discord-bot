package store

import (
	"context"
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

	must(s.SetMediaLook(ctx, horn, "🎺", pic))
	if m, _ := s.Media(ctx, horn); m.Emoji != "🎺" || m.IconID != pic {
		t.Errorf("look: %+v", m)
	}
	if err := s.SetMediaLook(ctx, 999, "x", 0); err != ErrNotFound {
		t.Errorf("look of a missing file: %v", err)
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
	must(s.SetPrefs(ctx, "ann", Prefs{ShowLabels: true}))
	if p, _ := s.Prefs(ctx, "ann"); p.PreviewVolume != 35 || !p.ShowLabels {
		t.Errorf("prefs: %+v", p)
	}
	if p, _ := s.Prefs(ctx, "bob"); p.PreviewVolume != 100 {
		t.Errorf("default preview volume: %+v", p)
	}
}
