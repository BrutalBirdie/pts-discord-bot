package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

func TestOutsideItemsRender(t *testing.T) {
	s := testServer(t)
	shared := store.Media{ID: 11, Name: "secret.ogg", Kind: store.KindSound}
	other := store.Media{ID: 12, Name: "other.ogg", Kind: store.KindSound, GuildID: "2"}
	avatar := store.Media{ID: 13, Name: "face.png", Kind: store.KindImage}
	msg := messages.Message{ID: 14, Name: "Hidden note"}
	task := tasks.Task{ID: 4, Name: "Horns", GuildID: "1", Busy: tasks.BusyWait, Trigger: tasks.Trigger{Kind: tasks.Manual},
		Steps: []tasks.Step{{Type: tasks.Play, Tag: "fun", MediaIDs: []int64{11, 12}, Volume: 100}, {Type: tasks.Profile, AvatarMediaID: 13},
			{Type: tasks.Message, ChannelID: "4", MessageID: 14}, {Type: tasks.Leave}}}
	render := func(sess *store.Session, e taskEditor) string {
		t.Helper()
		e.Task, e.StepTypes, e.Kinds, e.Weekdays, e.Page, e.Pages = task, tasks.StepTypes, tasks.TriggerKinds, tasks.WeekdayNames, 1, 1
		e.Outside = outside{discord: sess.Discord(), media: map[int64]store.Media{11: shared, 12: other, 13: avatar}, msgs: map[int64]messages.Message{14: msg}}
		if sess.Discord() {
			e.Outside.tags = []string{"fun"}
		}
		for _, st := range task.Steps {
			e.Issues = append(e.Issues, e.Outside.issues(st))
		}
		req := httptest.NewRequest("GET", "/tasks/4", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, sess))
		rec := httptest.NewRecorder()
		s.render(rec, req, http.StatusOK, "task", e)
		body := rec.Body.String()
		if strings.Contains(body, " style=") || strings.Contains(body, "\u2014") {
			t.Error("inline style attribute or em dash")
		}
		return body
	}

	body := render(&store.Session{Username: "discord:9", Provider: store.ProviderDiscord, Granted: []string{"1"}, CSRFToken: "tok"}, taskEditor{})
	for _, want := range []string{
		"Shared sound, only an admin can use it. Ask an admin to move it to this server.",
		"Shared sounds tagged fun, only an admin can use them. Ask an admin to move them to this server.",
		"Sound of another server, remove it.",
		"Shared image, only an admin can use it. Ask an admin to move it to this server.",
		"Shared message, only an admin can use it. Ask an admin to move it to this server.",
		`<input type="hidden" name="s0.media" value="11">`, `title="Shared sound"`, `<option value="13" selected>Shared image`, `<option value="14" selected>Shared message`,
		`<li class="step bad">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Discord editor: missing %q", want)
		}
	}
	for _, hidden := range []string{"secret.ogg", "other.ogg", "face.png", "Hidden note", "Deleted sound", "Deleted image", "/claim"} {
		if strings.Contains(body, hidden) {
			t.Errorf("Discord editor shows %q", hidden)
		}
	}

	body = render(&store.Session{Username: "ann", CSRFToken: "tok"}, taskEditor{Claim: claim{Names: []string{"sound secret.ogg"}, Server: "PTS", Warning: " These then fail to run: task Other (Lounge)."}})
	for _, want := range []string{"Sound other.ogg belongs to another server, remove it or move it to this server.", `action="/tasks/4/claim"`,
		"Move shared items of this task to PTS", `data-confirm-desc="Moves sound secret.ogg to PTS. Other servers can no longer use them. These then fail to run: task Other (Lounge)."`} {
		if !strings.Contains(body, want) {
			t.Errorf("Cloudron editor: missing %q", want)
		}
	}

	sess := &store.Session{Username: "ann", CSRFToken: "tok"}
	pages := map[string]any{
		"media": mediaData{Items: []store.Media{shared, other}, Counts: map[string]int{}, Move: true, Targets: []bot.Guild{{ID: "1", Name: "PTS"}, {ID: "2", Name: "Lounge"}},
			Warn: map[int64]string{11: "Used by task Horns (PTS)."}, BulkWarn: "Moves the selected files."},
		"message": messageEditor{Message: messages.Message{ID: 7, Name: "Friday"}, Limits: map[string]int{}, Move: true, Targets: []bot.Guild{{ID: "1", Name: "PTS"}}, MoveWarn: "Used by task Horns (PTS)."},
	}
	wants := map[string][]string{
		"media": {`action="/media/move" id="move-bulk"`, `form="move-bulk" aria-label="Select secret.ogg"`, "Move selected to server", `action="/media/11/move"`,
			`data-confirm-desc="Used by task Horns (PTS)."`, `<option value="2" selected>Lounge</option>`, `<option value="" selected>Shared, for Cloudron users only</option>`},
		"message": {`action="/messages/7/move"`, `popovertarget="sheet-move"`, `data-confirm-desc="Used by task Horns (PTS)."`, `<option value="" selected>Shared`},
	}
	for name, data := range pages {
		req := httptest.NewRequest("GET", "/", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, sess))
		rec := httptest.NewRecorder()
		s.render(rec, req, http.StatusOK, name, data)
		body := rec.Body.String()
		for _, want := range wants[name] {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %q", name, want)
			}
		}
		if strings.Contains(body, " style=") || strings.Contains(body, "\u2014") {
			t.Errorf("%s: inline style attribute or em dash", name)
		}
	}
}

func TestMoveSharedItems(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	id := func(n int64) string { return fmt.Sprint(n) }
	form := func(kv ...string) url.Values {
		v := url.Values{}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Add(kv[i], kv[i+1])
		}
		return v
	}
	addSound := func(name, owner string) int64 {
		m, err := f.st.AddMedia(ctx, store.Media{Name: name, Kind: store.KindSound, SizeBytes: 1, UploadedBy: "ann", GuildID: owner})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	horn := addSound("horn-"+f.tag+".ogg", "")
	clash := addSound("clash-"+f.tag+".ogg", "")
	addSound("clash-"+f.tag+".ogg", f.a)
	task, err := f.st.CreateTask(ctx, tasks.Task{Name: "horns-" + f.tag, GuildID: f.a, Busy: tasks.BusyWait, Trigger: tasks.Trigger{Kind: tasks.Manual},
		Steps: []tasks.Step{{Type: tasks.Play, MediaIDs: []int64{horn}, Volume: 100}, {Type: tasks.Message, ChannelID: "1", MessageID: f.msgS}}}, "ann")
	if err != nil {
		t.Fatal(err)
	}

	rec := f.do(f.discord, "GET", "/tasks/"+id(task), nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Shared sound, only an admin can use it. Ask an admin to move it to this server.") ||
		!strings.Contains(body, "Shared message, only an admin can use it.") || strings.Contains(body, "Deleted sound") {
		t.Errorf("Discord editor: %d", rec.Code)
	}
	save := form("op", "save", "guild", f.a, "name", "horns", "kind", "manual", "steps", "2", "s0.type", "play", "s0.media", id(horn), "s0.volume", "100",
		"s1.type", "message", "s1.channel", "1", "s1.message", id(f.msgS))
	rec = f.do(f.discord, "POST", "/tasks/"+id(task), save)
	if body := rec.Body.String(); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "Not saved.") ||
		!strings.Contains(body, "Step 1: Shared sound, only an admin can use it.") || !strings.Contains(body, "Step 2: Shared message") || strings.Contains(body, "was deleted") {
		t.Errorf("Discord save with shared items: %d", rec.Code)
	}
	if got, _ := f.st.Task(ctx, task); got.UpdatedBy != "ann" {
		t.Errorf("refused save changed updated_by to %s", got.UpdatedBy)
	}
	f.do(f.discord, "POST", "/tasks/"+id(task)+"/pause", form())
	if got, _ := f.st.Task(ctx, task); got.UpdatedBy != "ann" || got.Enabled {
		t.Errorf("a Discord pause changed updated_by to %s", got.UpdatedBy)
	}

	for _, target := range []string{"/media/" + id(horn) + "/move", "/media/move", "/messages/" + id(f.msgS) + "/move", "/messages/" + id(f.msgA) + "/move", "/tasks/" + id(task) + "/claim"} {
		if rec := f.do(f.discord, "POST", target, form("guild", f.a, "id", id(horn))); rec.Code != http.StatusForbidden {
			t.Errorf("Discord POST %s: %d, want 403", target, rec.Code)
		}
	}

	rec = f.do(f.cloud, "POST", "/media/move", form("guild", f.a, "id", id(horn), "id", id(clash)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "has a file named clash-"+f.tag+".ogg already") {
		t.Errorf("name clash: %d", rec.Code)
	}
	if m, _ := f.st.Media(ctx, horn); m.GuildID != "" {
		t.Errorf("refused bulk move moved horn to %q", m.GuildID)
	}

	rec = f.do(f.cloud, "GET", "/tasks/"+id(task), nil)
	if body := rec.Body.String(); !strings.Contains(body, `action="/tasks/`+id(task)+`/claim"`) || !strings.Contains(body, "sound horn-"+f.tag+".ogg") {
		t.Errorf("Cloudron editor offers no quick fix: %d", rec.Code)
	}
	if rec := f.do(f.cloud, "POST", "/media/"+id(horn)+"/move", form("guild", f.a)); rec.Code != http.StatusSeeOther {
		t.Errorf("move: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(f.cloud, "POST", "/tasks/"+id(task)+"/claim", form()); rec.Code != http.StatusSeeOther {
		t.Errorf("claim: %d", rec.Code)
	}
	if m, _ := f.st.Media(ctx, horn); m.GuildID != f.a {
		t.Errorf("horn owned by %q", m.GuildID)
	}
	if m, _ := f.st.Message(ctx, f.msgS); m.GuildID != f.a {
		t.Errorf("shared message owned by %q", m.GuildID)
	}
	audit, err := f.st.ListAudit(ctx, store.AuditQuery{Search: f.tag, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, e := range audit {
		actions[e.Action] = true
	}
	if !actions["media.move"] || !actions["message.move"] {
		t.Errorf("audit: %+v", audit)
	}

	rec = f.do(f.discord, "GET", "/tasks/"+id(task), nil)
	if body := rec.Body.String(); !strings.Contains(body, "horn-"+f.tag+".ogg") || strings.Contains(body, "Shared sound") || strings.Contains(body, "/claim") {
		t.Errorf("Discord editor after the move: %d", rec.Code)
	}
	if rec := f.do(f.discord, "POST", "/tasks/"+id(task), save); rec.Code != http.StatusSeeOther {
		t.Errorf("Discord save after the move: %d", rec.Code)
	}
}
