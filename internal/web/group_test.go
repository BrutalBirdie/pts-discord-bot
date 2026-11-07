package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

func TestServerGroups(t *testing.T) {
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
	horn, err := f.st.AddMedia(ctx, store.Media{Name: "horn-" + f.tag + ".ogg", Kind: store.KindSound, SizeBytes: 1, UploadedBy: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.do(f.cloud, "POST", "/media/group", form("guild", f.a, "group_only", "on", "id", id(horn), "id", id(f.medS))); rec.Code != http.StatusSeeOther {
		t.Fatalf("group: %d %s", rec.Code, rec.Body.String())
	}
	if m, _ := f.st.Media(ctx, horn); m.GuildID != f.a || !m.GroupOnly {
		t.Errorf("grouped horn: %+v", m)
	}
	audit, err := f.st.ListAudit(ctx, store.AuditQuery{Search: f.tag, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 2 || audit[0].Action != "media.group" || !strings.Contains(audit[0].Detail, "not suggested to other servers") {
		t.Errorf("audit: %+v", audit)
	}

	sounds := func(guild, q string) []string {
		t.Helper()
		rec := f.do(f.cloud, "GET", "/sounds?guild="+guild+"&q="+url.QueryEscape(q), nil)
		var hits []soundHit
		if err := json.Unmarshal(rec.Body.Bytes(), &hits); err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, h := range hits {
			out = append(out, h.Name)
		}
		return out
	}
	if got := sounds(f.a, "horn-"+f.tag); len(got) != 1 {
		t.Errorf("sound picker of A: %v", got)
	}
	if got := sounds(f.b, "horn-"+f.tag); len(got) != 0 {
		t.Errorf("sound picker of B suggests a sound A keeps to itself: %v", got)
	}
	if got := sounds(f.b, "horn-"+f.tag+".ogg"); len(got) != 1 {
		t.Errorf("sound picker of B, exact name: %v", got)
	}

	task, err := f.st.CreateTask(ctx, tasks.Task{Name: "horns-" + f.tag, GuildID: f.b, Busy: tasks.BusyWait, Trigger: tasks.Trigger{Kind: tasks.Manual},
		Steps: []tasks.Step{{Type: tasks.Play, MediaIDs: []int64{horn}, Volume: 100}, {Type: tasks.Message, ChannelID: "1", MessageID: f.msgA}}}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	rec := f.do(f.cloud, "GET", "/tasks/"+id(task), nil)
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "horn-"+f.tag+".ogg") || strings.Contains(body, "Deleted sound") {
		t.Errorf("editor of a task in B with a sound A keeps to itself: %d", rec.Code)
	}
	save := form("op", "save", "guild", f.b, "name", "horns", "kind", "manual", "steps", "2", "s0.type", "play", "s0.media", id(horn), "s0.volume", "100",
		"s1.type", "message", "s1.channel", "1", "s1.message", id(f.msgA))
	if rec := f.do(f.cloud, "POST", "/tasks/"+id(task), save); rec.Code != http.StatusSeeOther {
		t.Errorf("save: %d %s", rec.Code, rec.Body.String())
	}

	if rec := f.do(f.discord, "POST", "/messages/"+id(f.msgS), form("op", "save", "guild", f.a, "group_only", "on", "name", "msg-now-inA-"+f.tag, "content", "x")); rec.Code != http.StatusSeeOther {
		t.Errorf("Discord user groups a message: %d %s", rec.Code, rec.Body.String())
	}
	if m, _ := f.st.Message(ctx, f.msgS); m.GuildID != f.a || !m.GroupOnly {
		t.Errorf("grouped message: %+v", m)
	}
	if rec := f.do(f.discord, "POST", "/media/"+id(f.medB), form("name", "media-renamed-"+f.tag+".png", "guild", f.b)); rec.Code != http.StatusSeeOther {
		t.Errorf("Discord user edits a file of B, keeping its group: %d %s", rec.Code, rec.Body.String())
	}
	if m, _ := f.st.Media(ctx, f.medB); m.GuildID != f.b || m.Name != "media-renamed-"+f.tag+".png" {
		t.Errorf("edited file: %+v", m)
	}
	if rec := f.do(f.discord, "POST", "/media/"+id(f.medB), form("name", "media-shared-"+f.tag+".png")); rec.Code != http.StatusConflict {
		t.Errorf("rename to a name taken in another group: %d", rec.Code)
	}

	rec = f.do(f.cloud, "GET", "/media?server="+f.a+"&q="+f.tag, nil)
	body := rec.Body.String()
	for _, want := range []string{"horn-" + f.tag, "media-inA-" + f.tag, "only this server", `action="/media/group"`, "Set server group of selected"} {
		if !strings.Contains(body, want) {
			t.Errorf("media of A: missing %q", want)
		}
	}
	if strings.Contains(body, "media-renamed-"+f.tag) || strings.Contains(body, " style=") || strings.Contains(body, "—") {
		t.Error("media of A: file of B, inline style or em dash")
	}
	if rec := f.do(f.cloud, "GET", "/media?server=none&q="+f.tag, nil); strings.Contains(rec.Body.String(), "horn-"+f.tag) {
		t.Error("media without a group shows a grouped file")
	}
}

func TestRunNowAndStop(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	task, err := f.st.CreateTask(ctx, tasks.Task{Name: "greet-" + f.tag, GuildID: f.a, Busy: tasks.BusyWait,
		Trigger: tasks.Trigger{Kind: tasks.Event, Event: tasks.VoiceJoin, Cooldown: 30},
		Steps:   []tasks.Step{{Type: tasks.DM, ToTrigger: true, Text: "hi"}}}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprint(task)
	if rec := f.do(f.cloud, "GET", "/tasks/"+id, nil); !strings.Contains(rec.Body.String(), `aria-disabled="true" aria-label="Run now, not possible`) {
		t.Error("Run now is not disabled for a task that needs a member")
	}
	if rec := f.do(f.cloud, "POST", "/tasks/"+id+"/run", url.Values{}); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), runNeedsMember) {
		t.Errorf("run a task that needs a member: %d", rec.Code)
	}
	runA, err := f.st.StartRun(ctx, store.Run{Title: "run-" + f.tag, GuildID: f.a, Source: store.SourceManual})
	if err != nil {
		t.Fatal(err)
	}
	runB, err := f.st.StartRun(ctx, store.Run{Title: "run-" + f.tag, GuildID: f.b, Source: store.SourceManual})
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.do(f.discord, "POST", "/runs/"+fmt.Sprint(runB)+"/stop", url.Values{}); rec.Code != http.StatusNotFound {
		t.Errorf("Discord user stops a run of server B: %d", rec.Code)
	}
	if rec := f.do(f.discord, "POST", "/runs/"+fmt.Sprint(runA)+"/stop", url.Values{}); rec.Code != http.StatusConflict {
		t.Errorf("stop a run this app is not running: %d", rec.Code)
	}
	if rec := f.do(f.cloud, "GET", "/", nil); !strings.Contains(rec.Body.String(), `action="/runs/`+fmt.Sprint(runA)+`/stop"`) {
		t.Error("dashboard has no Stop button for a running run")
	}
}
