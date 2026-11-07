package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

func TestSoundLookAndFavorite(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	horn, err := f.st.AddMedia(ctx, store.Media{Name: "horn-" + f.tag + ".ogg", Kind: store.KindSound, SizeBytes: 1, UploadedBy: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	post := func(cookie, path string, kv ...string) int {
		v := url.Values{}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return f.do(cookie, "POST", path, v).Code
	}
	edit := fmt.Sprintf("/media/%d/sound", horn)
	if code := post(f.discord, edit, "name", "horn-"+f.tag+".ogg", "display_name", "Big Horn", "emoji", "🎺", "trim_start", "0.5", "trim_end", "1.25", "own_volume", "on", "volume", "150", "next", "/room/1"); code != http.StatusSeeOther {
		t.Errorf("edit by a Discord user: %d", code)
	}
	if m, _ := f.st.Media(ctx, horn); m.Label() != "Big Horn" || m.Emoji != "🎺" || m.TrimStartMS != 500 || m.TrimEndMS != 1250 || m.Volume != 150 {
		t.Errorf("edited: %+v", m)
	}
	if code := post(f.cloud, edit, "emoji", "<:salt:123456789012345678>", "icon", fmt.Sprint(f.medS)); code != http.StatusSeeOther {
		t.Errorf("edit with an image: %d", code)
	}
	if m, _ := f.st.Media(ctx, horn); m.Emoji != "" || m.IconID != f.medS || m.Volume != 0 || m.TrimStartMS != 0 || m.Name != "horn-"+f.tag+".ogg" {
		t.Errorf("image, own volume off, name kept: %+v", m)
	}
	if code := post(f.cloud, edit, "emoji", "<:salt:123456789012345678>"); code != http.StatusSeeOther {
		t.Errorf("custom emoji: %d", code)
	}
	for _, bad := range [][]string{{"icon", fmt.Sprint(f.trashS)}, {"emoji", "not an emoji at all"}, {"emoji", "<img src=x>"}, {"emoji", "E"}, {"trim_start", "2", "trim_end", "1"},
		{"own_volume", "on", "volume", "300"}, {"name", "media-shared-" + f.tag + ".png"}} {
		if code := post(f.cloud, edit, bad...); code != http.StatusBadRequest && code != http.StatusConflict {
			t.Errorf("%v: %d", bad, code)
		}
	}
	if code := post(f.cloud, "/prefs/play-volume", "volume", "150"); code != http.StatusNoContent {
		t.Errorf("play volume: %d", code)
	}

	fav := fmt.Sprintf("/media/%d/favorite", horn)
	if code := post(f.discord, fav, "on", "1"); code != http.StatusSeeOther {
		t.Errorf("favorite: %d", code)
	}
	if favs, _ := f.st.FavoriteSounds(ctx, "ann"); len(favs) != 0 {
		t.Errorf("favorite of another user: %v", favs)
	}
	if code := post(f.cloud, "/media/999999999/favorite", "on", "1"); code != http.StatusNotFound {
		t.Errorf("favorite of a missing sound: %d", code)
	}
	if code := post(f.cloud, "/prefs/preview-volume", "volume", "35"); code != http.StatusNoContent {
		t.Errorf("preview volume: %d", code)
	}
	if code := post(f.cloud, "/prefs/preview-volume", "volume", "101"); code != http.StatusBadRequest {
		t.Errorf("preview volume over 100: %d", code)
	}
	if p, _ := f.st.Prefs(ctx, "ann"); p.PreviewVolume != 35 {
		t.Errorf("prefs: %+v", p)
	}
	if code := f.do(f.cloud, "GET", "/room/1", nil).Code; code != http.StatusNotFound {
		t.Errorf("room of an unknown server: %d", code)
	}
}

func TestPrefillAndTaskRelations(t *testing.T) {
	members := []bot.Member{{ID: "9", Name: "Mira", Username: "mira"}}
	var tk tasks.Task
	prefill(&tk, url.Values{"member": {"9"}, "join": {"2"}, "play": {"5"}, "post": {"4"}}, members)
	types := []tasks.StepType{}
	for _, st := range tk.Steps {
		types = append(types, st.Type)
	}
	if tk.Name != "Greet Mira" || tk.Trigger.Kind != tasks.Event || tk.Trigger.Event != tasks.VoiceJoin || !slices.Equal(tk.Trigger.UserIDs, []string{"9"}) ||
		!slices.Equal(types, []tasks.StepType{tasks.Join, tasks.Join, tasks.Play, tasks.Message}) || !tk.Steps[0].ToTrigger || tk.Steps[2].Volume != 100 {
		t.Errorf("prefill: %+v", tk)
	}

	greet := tasks.Task{Trigger: tasks.Trigger{Kind: tasks.Event, Event: tasks.VoiceJoin, UserIDs: []string{"9"}}, Steps: []tasks.Step{{Type: tasks.Join, ToTrigger: true}}}
	if only, except := memberTasks(greet, "9"); !only || except {
		t.Error("greet is not for Mira")
	}
	greet.Trigger.Except = true
	if only, except := memberTasks(greet, "9"); only || !except || startsFor(greet.Trigger, "9") || !startsFor(greet.Trigger, "8") {
		t.Error("except filter")
	}
	dm := tasks.Task{Trigger: tasks.Trigger{Kind: tasks.Manual}, Steps: []tasks.Step{{Type: tasks.DM, UserID: "9"}}}
	if only, _ := memberTasks(dm, "9"); !only {
		t.Error("DM step does not name Mira")
	}
	if !usesChannel(tasks.Task{Steps: []tasks.Step{{Type: tasks.Join, ChannelID: "2"}}}, "2") || usesChannel(tasks.Task{Trigger: tasks.Trigger{Kind: tasks.Daily, ChannelID: "2"}}, "2") ||
		!usesChannel(tasks.Task{Trigger: tasks.Trigger{Kind: tasks.Event, ChannelID: "2"}}, "2") {
		t.Error("usesChannel")
	}
}

func TestMessageButtons(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	form := func(kv ...string) url.Values {
		v := url.Values{"op": {"save"}, "name": {"Raid " + f.tag}, "content": {"Raid tonight?"}, "embeds": {"0"}}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}
	rec := f.do(f.discord, "POST", "/messages/new", form("buttons", "2",
		"b0.label", "I am in", "b0.kind", "task", "b0.task", fmt.Sprint(f.taskA), "b0.style", "success", "b0.reply", "On the list, {member_name}.",
		"b1.label", "Rules", "b1.kind", "link", "b1.url", "https://example.com/rules", "b1.task", fmt.Sprint(f.taskA)))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	list, err := f.st.FindMessages(ctx, store.MessageQuery{Search: "Raid " + f.tag})
	if err != nil || len(list) != 1 {
		t.Fatalf("saved: %v %v", list, err)
	}
	b := list[0].Buttons
	if len(b) != 2 || b[0].TaskID != f.taskA || b[0].Style != "success" || b[0].Reply == "" || !b[1].IsLink() || b[1].URL != "https://example.com/rules" || b[1].Reply != "" {
		t.Errorf("buttons: %+v", b)
	}
	if rec := f.do(f.discord, "POST", "/messages/new", form("buttons", "1", "b0.label", "Theirs", "b0.kind", "task", "b0.task", fmt.Sprint(f.taskB))); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "Button 1: pick one of your tasks.") {
		t.Errorf("task of another server: %d", rec.Code)
	}
	if rec := f.do(f.cloud, "POST", "/messages/new", form("buttons", "1", "b0.kind", "link", "b0.url", "ftp://x")); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "Button 1: give it a label or an emoji.") || !strings.Contains(rec.Body.String(), "starts with https://") {
		t.Errorf("bad link button: %d", rec.Code)
	}
	page := f.do(f.discord, "GET", fmt.Sprintf("/messages/%d", list[0].ID), nil).Body.String()
	for _, want := range []string{`name="b0.label" value="I am in"`, `<span class="dbtn" data-style="success"`, `data-style="link"`, `value="up-button:1"`, `name="b1.url" value="https://example.com/rules"`} {
		if !strings.Contains(page, want) {
			t.Errorf("editor misses %q", want)
		}
	}
}

func TestMemberList(t *testing.T) {
	var all []bot.Member
	for i := range 30 {
		m := bot.Member{ID: fmt.Sprint(i), Name: fmt.Sprintf("m%02d", i), Username: fmt.Sprintf("u%02d", i), Online: i%2 == 0, Status: "online"}
		if !m.Online {
			m.Status = "offline"
		}
		if i >= 25 {
			m.VoiceID = "2"
		}
		if i < 4 {
			m.Group, m.GroupPos = "Admins", 5
		}
		if i == 4 {
			m.Group, m.GroupPos = "Mods", 3
		}
		if i == 24 {
			m.Group, m.GroupPos = "Owner", 9
		}
		all = append(all, m)
	}
	all = append(all, bot.Member{ID: "99", Name: "Bot", Bot: true, Online: true})
	names := func(gs []memberGroup) []string {
		var out []string
		for _, g := range gs {
			out = append(out, fmt.Sprintf("%s:%d", g.Name, len(g.Members)))
		}
		return out
	}
	groups, hidden, _ := memberList(all, "", 1, true)
	if got := names(groups); !slices.Equal(got, []string{"Owner:1", "Admins:2", "Mods:1", "Online:11", "Offline:3"}) || hidden != 12 {
		t.Errorf("groups %v, hidden %d", got, hidden)
	}
	if groups, _, _ := memberList(all, "", 1, false); !slices.Equal(names(groups), []string{"Owner:1", "Admins:4", "Mods:1", "Members:14"}) {
		t.Errorf("without presences: %v", names(groups))
	}
	groups, _, more := memberList(all, "m1", 1, true)
	if !slices.Equal(names(groups), []string{"Matches:10"}) || more {
		t.Errorf("search: %v %v", names(groups), more)
	}
	if groups, _, more := memberList(all, "u", 2, true); !slices.Equal(names(groups), []string{"Matches:10"}) || more {
		t.Errorf("second page: %v %v", names(groups), more)
	}
}

func TestValidEmoji(t *testing.T) {
	for _, ok := range []string{"", "🔥", "☀️", "👨‍👩‍👧", "🇩🇪", "1️⃣", "<:salt:123456789012345678>", "<a:dance:123456789012345678>"} {
		if !validEmoji(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"E", "ab", "🔥 x", "<:x:1>", "<b>", strings.Repeat("🔥", 11)} {
		if validEmoji(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPlaceAt(t *testing.T) {
	all := []string{"a", "b", "c", "d", "e"}
	cases := []struct {
		moved, want []string
	}{
		{[]string{"c", "b"}, []string{"a", "c", "b", "d", "e"}},
		{[]string{"e", "a", "c"}, []string{"e", "b", "a", "d", "c"}},
		{[]string{"x", "b", "b", "a"}, []string{"b", "a", "c", "d", "e"}},
		{nil, all},
	}
	for _, c := range cases {
		if got := placeAt(all, c.moved); !slices.Equal(got, c.want) {
			t.Errorf("%v: %v", c.moved, got)
		}
	}
	gs := []bot.Guild{{ID: "1", Name: "A"}, {ID: "2", Name: "B"}, {ID: "3", Name: "C"}}
	got := orderGuilds(gs, []string{"3", "9", "1"})
	if got[0].ID != "3" || got[1].ID != "1" || got[2].ID != "2" {
		t.Errorf("orderGuilds: %v", got)
	}
}
