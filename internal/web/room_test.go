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
	look := fmt.Sprintf("/media/%d/look", horn)
	if code := post(f.discord, look, "emoji", "🎺", "next", "/room/1"); code != http.StatusSeeOther {
		t.Errorf("look by a Discord user: %d", code)
	}
	if m, _ := f.st.Media(ctx, horn); m.Emoji != "🎺" {
		t.Errorf("emoji: %+v", m)
	}
	if code := post(f.cloud, look, "emoji", "🎺", "emoji_text", "🔥", "icon", fmt.Sprint(f.medS)); code != http.StatusSeeOther {
		t.Errorf("look with an image: %d", code)
	}
	if m, _ := f.st.Media(ctx, horn); m.Emoji != "" || m.IconID != f.medS {
		t.Errorf("image: %+v", m)
	}
	if code := post(f.cloud, look, "icon", fmt.Sprint(f.trashS)); code != http.StatusBadRequest {
		t.Errorf("a sound as image: %d", code)
	}
	if code := post(f.cloud, look, "emoji_text", "not an emoji at all"); code != http.StatusBadRequest {
		t.Errorf("long text as emoji: %d", code)
	}
	if code := post(f.cloud, look, "clear", "1", "icon", fmt.Sprint(f.medS)); code != http.StatusSeeOther {
		t.Errorf("clear: %d", code)
	}
	if m, _ := f.st.Media(ctx, horn); m.Emoji != "" || m.IconID != 0 {
		t.Errorf("cleared: %+v", m)
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
