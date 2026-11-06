package media

import (
	"context"
	"strings"
	"testing"
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

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{"friday card.png": "friday_card.png", `C:\x\a.ogg`: "a.ogg", "../../etc/passwd": "passwd", ".hidden": "hidden"} {
		if got, err := CleanName(in); err != nil || got != want {
			t.Errorf("%q: got %q %v", in, got, err)
		}
	}
}
