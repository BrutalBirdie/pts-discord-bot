package messages

import (
	"strings"
	"testing"
	"time"
)

func TestProblems(t *testing.T) {
	ok := Message{Name: "Friday", Content: "Game night", Embeds: []Embed{{Title: "Game night", Color: "#4752c4", Fields: []Field{{Name: "Start", Value: "20:00"}}}}}
	if p := ok.Problems(); len(p) != 0 {
		t.Errorf("valid message: %v", p)
	}
	bad := Message{Name: "x", Content: strings.Repeat("ä", 2001), Embeds: []Embed{{}, {Title: "t", Color: "blue", Fields: []Field{{Name: "n"}}}}}
	p := bad.Problems()
	for _, want := range []string{"2001 characters", "Embed 1: it is empty", "#4752c4", "field 1 needs"} {
		found := false
		for _, s := range p {
			found = found || strings.Contains(s, want)
		}
		if !found {
			t.Errorf("missing problem %q in %v", want, p)
		}
	}
}

func TestEmbedChars(t *testing.T) {
	m := Message{Embeds: []Embed{{Title: "ab", Description: "cd", Footer: "e", Fields: []Field{{Name: "f", Value: "gh"}}}}}
	if n := m.EmbedChars(); n != 8 {
		t.Errorf("got %d", n)
	}
}

func TestExpand(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	got := Expand("{date} {time} in {channel} on {server} {unknown}", Vars{Now: now, Channel: "#general", Server: "PTS"})
	if got != "2026-11-06 12:00 in #general on PTS {unknown}" {
		t.Errorf("got %q", got)
	}
}

func TestColorInt(t *testing.T) {
	if (Embed{Color: "#4752c4"}).ColorInt() != 0x4752c4 {
		t.Error("color")
	}
}
