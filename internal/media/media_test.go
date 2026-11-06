package media

import (
	"context"
	"strings"
	"testing"

	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

func TestPublicOnly(t *testing.T) {
	for addr, ok := range map[string]bool{
		"93.184.215.14:443": true, "[2606:4700::1111]:443": true,
		"127.0.0.1:80": false, "10.1.2.3:80": false, "172.18.0.1:3000": false, "192.168.1.1:80": false,
		"169.254.169.254:80": false, "100.64.1.1:80": false, "[::1]:80": false, "[fd00::1]:80": false, "0.0.0.0:80": false,
		"[::ffff:127.0.0.1]:80": false,
	} {
		if got := publicOnly("tcp", addr, nil) == nil; got != ok {
			t.Errorf("%s: allowed=%v, want %v", addr, got, ok)
		}
	}
}

func TestFetchRefusesLocal(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:1/x.ogg", "file:///etc/passwd", "ftp://example.com/a", "not a url"} {
		if _, _, err := Fetch(context.Background(), u); err == nil {
			t.Errorf("%s: fetched", u)
		} else if strings.Contains(u, "127.0.0.1") && !strings.Contains(err.Error(), "not a public address") {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestAvatarProblem(t *testing.T) {
	for _, c := range []struct {
		m    store.Media
		want string
	}{
		{store.Media{Name: "cat.png", Kind: store.KindImage, ContentType: "image/png", SizeBytes: 1 << 20}, ""},
		{store.Media{Name: "cat.webp", Kind: store.KindImage, ContentType: "image/webp", SizeBytes: tasks.MaxAvatarBytes}, ""},
		{store.Media{Name: "big.gif", Kind: store.KindImage, ContentType: "image/gif", SizeBytes: tasks.MaxAvatarBytes + 1}, "big.gif has 8.0 MB, an avatar may have at most 8 MB"},
		{store.Media{Name: "a.svg", Kind: store.KindFile, ContentType: "image/svg+xml", SizeBytes: 10}, "a.svg is not a PNG, JPEG, GIF or WebP image"},
		{store.Media{Name: "a.ogg", Kind: store.KindSound, ContentType: "audio/ogg", SizeBytes: 10}, "a.ogg is not a PNG, JPEG, GIF or WebP image"},
	} {
		if got := AvatarProblem(c.m); got != c.want {
			t.Errorf("%s: got %q, want %q", c.m.Name, got, c.want)
		}
	}
	if AvatarType([]byte("\x89PNG\r\n\x1a\n0000")) != "image/png" || AvatarType([]byte("<svg></svg>")) != "" {
		t.Error("avatar type detection")
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{"friday card.png": "friday_card.png", `C:\x\a.ogg`: "a.ogg", "../../etc/passwd": "passwd", ".hidden": "hidden"} {
		if got, err := CleanName(in); err != nil || got != want {
			t.Errorf("%q: got %q %v", in, got, err)
		}
	}
}
