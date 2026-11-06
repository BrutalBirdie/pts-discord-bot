package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/BrutalBirdie/pts-discord-bot/internal/access"
	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

func testServer(t *testing.T) *Server {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(Config{Origin: "https://bot.example", Admins: access.Parse("ann", "bot-admins")}, nil, bot.New(bot.Config{Logger: log}), nil, log)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPagesRender(t *testing.T) {
	s := testServer(t)
	admin := &store.Session{Username: "ann", DisplayName: "Ann Admin", IsAdmin: true, CSRFToken: "tok"}
	now := time.Now()
	taskID := int64(3)
	step := 2
	runs := []store.Run{
		{ID: 1, TaskID: &taskID, Title: "Adhan", Source: store.SourceSchedule, Status: store.RunDone, StartedAt: now.Add(-time.Hour), FinishedAt: &now},
		{ID: 2, Title: "Play x.ogg", Source: store.SourceManual, Actor: "ann", Status: store.RunFailed, FailedStep: &step, Error: "Discord error 50001: Missing Access", StartedAt: now},
	}
	adhan := tasks.Task{ID: 3, Name: "Adhan", GuildID: "1", Enabled: true, Busy: tasks.BusyWait,
		Trigger: tasks.Trigger{Kind: tasks.Daily, Times: []string{"05:00", "16:35"}, Timezone: "Europe/Berlin"},
		Steps: []tasks.Step{{Type: tasks.Join, ChannelID: "2"}, {Type: tasks.Play, MediaID: 5, Volume: 80}, {Type: tasks.Message, ChannelID: "4", Text: "Adhan at {time}"},
			{Type: tasks.DM, UserID: "9"}, {Type: tasks.Wait, Seconds: 5}, {Type: tasks.Leave}}}
	sound := store.Media{ID: 5, Name: "muezzin.ogg", Kind: store.KindSound, SizeBytes: 2 << 20, DurationMS: 225000, UsedBy: []string{"task Adhan"}}
	img := store.Media{ID: 6, Name: "friday.jpg", Kind: store.KindImage, SizeBytes: 1 << 20, Width: 1600, Height: 900}
	msg := messages.Message{ID: 7, Name: "Friday", Content: "Hi <@123456789012345678> {date}", Files: []int64{6},
		Embeds: []messages.Embed{{Title: "Prayer", Color: "#112233", ImageMediaID: 6, Fields: []messages.Field{{Name: "Khutbah", Value: "13:15", Inline: true}}}}}

	type page struct {
		name string
		data any
		want []string
	}
	pages := []page{
		{"login", nil, []string{"Sign in with Cloudron"}},
		{"error", nil, []string{"Something went wrong"}},
		{"dashboard", dashboardData{Upcoming: []upcoming{{At: now.Add(time.Hour), Task: adhan, Guild: "PTS", What: "Join prayer"}}, Runs: runs, Runs24h: 2, Failed24h: 1,
			Playing: []playingView{{P: bot.Playing{GuildID: "1", Name: "muezzin.ogg", Channel: "prayer", Started: now, Duration: time.Minute}, CSRF: "tok", Next: "/"}}},
			[]string{"Adhan", "Missing Access", `action="/servers/1/stop"`, "muezzin.ogg", "of 2 runs"}},
		{"servers", serversData{Guilds: []bot.Guild{{ID: "1", Name: "PTS Server", MemberCount: 12, Voice: 2, Text: 3, Problems: 1}}, InviteURL: "https://discord.com/oauth2/authorize?client_id=1"},
			[]string{`href="/servers/1"`, ">PS<", "1 channel without access", "client_id=1"}},
		{"server", serverData{Guild: bot.Guild{ID: "1", Name: "PTS"}, Tab: "channels", Voice: []bot.Channel{{ID: "2", Name: "prayer", Voice: true, Usable: true, HasBot: true, InVoice: []string{"Amira"}}},
			Text: []bot.Channel{{ID: "4", Name: "rules", Problem: "The bot needs View Channel and Send Messages here."}}, Sounds: []store.Media{sound}},
			[]string{`id="sheet-play-2"`, "Bot and Amira connected", "read only", `value="5"`}},
		{"tasks", tasksData{Rows: []taskRow{{Task: adhan, Guild: "PTS", What: "Join prayer", Next: &now, Last: &runs[0]}}, Counts: map[string]int{"": 1, "active": 1}},
			[]string{"Daily 05:00, 16:35", `action="/tasks/3/pause"`, "Active 1"}},
		{"task", taskEditor{Task: adhan, StepTypes: tasks.StepTypes, Kinds: tasks.TriggerKinds, Weekdays: tasks.WeekdayNames, TimesText: "05:00 16:35",
			Voice: []bot.Channel{{ID: "2", Name: "prayer", Voice: true}}, Text: []bot.Channel{{ID: "44", Name: "general"}}, Labels: map[string]string{"9": "Amira (@amira)"},
			Sounds: []store.Media{sound}, Messages: []messages.Message{msg}, Next: adhan.Trigger.Next(now, 3), Runs: runs, RunCount: 2, Page: 1, Pages: 1},
			[]string{`name="s0.target"`, `value="2" selected`, `name="s0.follow" value=""`, `data-members="1"`, `value="5" selected`, `name="s1.volume" value="80"`, `value="add:wait"`, `value="daily" checked`, "Unknown channel 4", `name="s3.user" value="Amira (@amira)"`, `name="s4.wait" value="5s"`, `action="/tasks/3/delete"`, "As cron"}},
		{"task", taskEditor{Task: tasks.Task{Trigger: tasks.Trigger{Kind: tasks.Manual}}, New: true, StepTypes: tasks.StepTypes, Kinds: tasks.TriggerKinds, Weekdays: tasks.WeekdayNames, Problems: []string{"Give the task a name."}},
			[]string{"Create task", "Give the task a name.", `action="/tasks/new"`}},
		{"messages", []messageRow{{Message: msg, UsedBy: []string{"Adhan"}}}, []string{`href="/messages/7"`, "1 embed", "1 file"}},
		{"message", messageEditor{Message: msg, Media: []store.Media{sound, img}, ByID: map[int64]store.Media{5: sound, 6: img}, Embeddable: map[int64]bool{6: true},
			Names: map[string]string{"123456789012345678": "Amira"}, Limits: map[string]int{"content": 2000, "embeds": 10, "embedTotal": 6000},
			Channels: []guildChannels{{Guild: bot.Guild{Name: "PTS"}, Text: []bot.Channel{{ID: "4", Name: "general"}}}}},
			[]string{`<span class="dmention">@Amira</span>`, `fill="#112233"`, `name="e0.f0.inline" checked`, `src="/media/6/file"`, `action="/messages/7/send"`, `name="file" value="6"`}},
		{"media", mediaData{Items: []store.Media{sound, img, {ID: 8, Name: "big.mp4", Kind: store.KindVideo, SizeBytes: 30 << 20}}, Counts: map[string]int{"": 3, "sound": 1}, MaxUploadMB: 100,
			Voice: []voiceGroup{{Guild: bot.Guild{Name: "PTS"}, Voice: []bot.Channel{{ID: "2", Name: "prayer"}}}}},
			[]string{`id="sheet-play-5"`, `<audio controls preload="none" src="/media/5/file">`, "over 20 MB", "used by task Adhan", "3:45", `enctype="multipart/form-data"`}},
		{"admin", adminData{Tab: "overview", Admins: []string{"ann"}, Groups: []string{"bot-admins"}}, []string{"BOT_ADMIN_GROUP", "bot-admins"}},
		{"admin", adminData{Tab: "settings", Settings: store.DefaultSettings, Timezones: commonTimezones}, []string{`name="max_play_minutes" value="10"`, `value="Europe/Berlin"`}},
		{"admin", adminData{Tab: "audit", Page: 1, Audit: []store.AuditEntry{{At: now, Actor: "ann", Action: "task.run", Target: "Adhan"}}}, []string{"task.run"}},
	}
	for _, p := range pages {
		req := httptest.NewRequest("GET", "/?done=saved", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, admin))
		rec := httptest.NewRecorder()
		s.render(rec, req, http.StatusOK, p.name, p.data)
		body := rec.Body.String()
		if !strings.Contains(body, "</html>") {
			t.Errorf("%s: incomplete page:\n%s", p.name, body)
			continue
		}
		for _, w := range append(p.want, "Saved.") {
			if !strings.Contains(body, w) {
				t.Errorf("%s: missing %q", p.name, w)
			}
		}
		if strings.Contains(body, " style=") {
			t.Errorf("%s: inline style attribute, blocked by the CSP", p.name)
		}
	}
}

func TestParseTask(t *testing.T) {
	form := url.Values{
		"name": {" Adhan "}, "guild": {"1"}, "enabled": {"on"}, "busy": {"skip"}, "kind": {"weekly"}, "times": {"16:35, 5:00"}, "weekday": {"5", "1", "5"},
		"timezone": {"Europe/Berlin"}, "cron": {"0 1 * * *"}, "steps": {"5"},
		"s0.type": {"join"}, "s0.target": {"member"}, "s0.follow": {" Amira (@amira) "},
		"s1.type": {"play"}, "s1.media": {"5"}, "s1.volume": {"80"},
		"s2.type": {"dm"}, "s2.user": {"<@123>"}, "s2.message": {"0"}, "s2.text": {"hi"}, "s2.channel": {"4"},
		"s3.type": {"wait"}, "s3.wait": {"1.5s"}, "s3.user": {"Jane"}, "s3.channel": {"2"},
		"s4.type": {"bogus"},
	}
	r := httptest.NewRequest("POST", "/tasks/new", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	task, bad := parseTask(r)
	if len(bad) != 0 {
		t.Fatalf("problems: %v", bad)
	}
	if task.Name != "Adhan" || task.Busy != tasks.BusySkip || task.Trigger.Kind != tasks.Weekly {
		t.Errorf("basics: %+v", task)
	}
	if len(task.Trigger.Cron) != 0 || len(task.Trigger.Weekdays) != 2 || task.Trigger.Times[0] != "05:00" {
		t.Errorf("trigger: %+v", task.Trigger)
	}
	if len(task.Steps) != 4 || task.Steps[0].FollowUserID != "Amira (@amira)" || task.Steps[2].UserID != "<@123>" || task.Steps[2].ChannelID != "" ||
		task.Steps[3].WaitMS != 1500 || task.Steps[3].UserID != "" || task.Steps[3].ChannelID != "" {
		t.Errorf("steps: %+v", task.Steps)
	}
	applyOp(&task, "up:1")
	if task.Steps[0].Type != tasks.Play {
		t.Errorf("up: %+v", task.Steps)
	}
	applyOp(&task, "del:0")
	applyOp(&task, "add:leave")
	if len(task.Steps) != 4 || task.Steps[3].Type != tasks.Leave {
		t.Errorf("del/add: %+v", task.Steps)
	}
}

func TestParseMessage(t *testing.T) {
	form := url.Values{"name": {"F"}, "content": {"a\r\nb"}, "embeds": {"1"}, "e0.title": {"T"}, "e0.color": {"#000000"}, "e0.fields": {"1"},
		"e0.f0.name": {"n"}, "e0.f0.value": {"v"}, "e0.f0.inline": {"on"}, "e0.image": {"6"}, "file": {"6", "6", "7"}}
	r := httptest.NewRequest("POST", "/messages/new", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	m := parseMessage(r)
	if m.Content != "a\nb" || len(m.Embeds) != 1 || !m.Embeds[0].Fields[0].Inline || m.Embeds[0].ImageMediaID != 6 || len(m.Files) != 2 {
		t.Errorf("got %+v", m)
	}
	applyMessageOp(&m, "del-field:0.0", r)
	applyMessageOp(&m, "add-embed", r)
	if len(m.Embeds[0].Fields) != 0 || len(m.Embeds) != 2 {
		t.Errorf("ops: %+v", m)
	}
	if ids := attachedIDs(m); len(ids) != 2 {
		t.Errorf("attached %v", ids)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"/tasks": "/tasks", "//evil.example": "/", "https://evil.example": "/", "/a\\b": "/"} {
		if got := safeNext(in, "/"); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
}

func TestWhen(t *testing.T) {
	SetDisplayTimezone("UTC")
	now := time.Now().UTC()
	if got := when(now); !strings.HasPrefix(got, "Today ") {
		t.Errorf("today: %q", got)
	}
	if got := when(now.Add(24 * time.Hour)); !strings.HasPrefix(got, "Tomorrow ") {
		t.Errorf("tomorrow: %q", got)
	}
	if got := clock(225000); got != "3:45" {
		t.Errorf("clock: %q", got)
	}
}

func TestParseEventTask(t *testing.T) {
	form := url.Values{
		"name": {"Greeting"}, "guild": {"1"}, "slash": {"on"}, "kind": {"event"}, "event": {"message"}, "event_channel": {"5"}, "keyword": {" adhan "},
		"cooldown": {"60"}, "times": {"05:00"}, "steps": {"3"}, "ev_member": {" Amira (@amira) ", "", "Amira (@amira)", "Bilal"},
		"s0.type": {"join"}, "s0.target": {"trigger"},
		"s1.type": {"play"}, "s1.media": {"tag:greeting"}, "s1.volume": {"100"},
		"s2.type": {"dm"}, "s2.to": {"trigger"}, "s2.user": {"Amira"}, "s2.message": {"0"}, "s2.text": {"Hi {member}"},
	}
	r := httptest.NewRequest("POST", "/tasks/new", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	task, bad := parseTask(r)
	if len(bad) != 0 {
		t.Fatalf("problems: %v", bad)
	}
	tr := task.Trigger
	if !task.Slash || tr.Kind != tasks.Event || tr.Event != tasks.MessageIn || tr.ChannelID != "5" || tr.Keyword != "adhan" || tr.Cooldown != 60 || len(tr.Times) != 0 || strings.Join(tr.UserIDs, "|") != "Amira (@amira)|Bilal" {
		t.Errorf("trigger: %+v slash=%v", tr, task.Slash)
	}
	if !task.Steps[0].ToTrigger || task.Steps[1].Tag != "greeting" || task.Steps[1].MediaID != 0 || !task.Steps[2].ToTrigger || task.Steps[2].UserID != "" {
		t.Errorf("steps: %+v", task.Steps)
	}
	if p := task.Problems(); len(p) != 0 {
		t.Errorf("problems: %v", p)
	}
	applyOp(&task, "evdel:0")
	if strings.Join(task.Trigger.UserIDs, "|") != "Bilal" {
		t.Errorf("evdel: %v", task.Trigger.UserIDs)
	}
}

func TestParseTags(t *testing.T) {
	got := ParseTags(" Greeting, funny   Sounds,greeting,, ")
	if strings.Join(got, "|") != "funny sounds|greeting" {
		t.Errorf("got %q", got)
	}
}

func TestEventEditorRenders(t *testing.T) {
	s := testServer(t)
	sess := &store.Session{Username: "ann", CSRFToken: "tok"}
	task := tasks.Task{ID: 4, Name: "Greeting", GuildID: "1", Slash: true, Busy: tasks.BusyWait,
		Trigger: tasks.Trigger{Kind: tasks.Event, Event: tasks.VoiceJoin, ChannelID: "2", Cooldown: 45, UserIDs: []string{"9"}},
		Steps:   []tasks.Step{{Type: tasks.Join, ToTrigger: true}, {Type: tasks.Play, Tag: "greeting", Volume: 100}, {Type: tasks.DM, ToTrigger: true, Text: "Hi {member}"}}}
	e := taskEditor{Task: task, StepTypes: tasks.StepTypes, Kinds: tasks.TriggerKinds, Weekdays: tasks.WeekdayNames, Events: tasks.EventTypes, Tags: []string{"greeting"},
		Voice: []bot.Channel{{ID: "2", Name: "lounge", Voice: true}}, Labels: map[string]string{}, EvMembers: []bot.Member{{ID: "9", Name: "amira", Username: "amira"}}, Page: 1, Pages: 1}
	req := httptest.NewRequest("GET", "/tasks/4", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, sess))
	rec := httptest.NewRecorder()
	s.render(rec, req, http.StatusOK, "task", e)
	body := rec.Body.String()
	for _, want := range []string{`value="event" checked`, `<option value="voice_join" selected>A member joins a voice channel`, `<option value="2" selected>lounge`,
		`name="cooldown" value="45"`, `<input type="hidden" name="ev_member" value="9">`, `value="evdel:0"`, `<span class="av" aria-hidden="true">A</span>`, "1 of 25", `name="slash" checked`, `<option value="trigger" selected>The channel of the member who started the run`,
		`<option value="tag:greeting" selected>Random: greeting`, `<option value="trigger" selected>To the member who started the run`, "Runs when the event happens."} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestWaitUntil(t *testing.T) {
	form := url.Values{"name": {"Greeting"}, "guild": {"1"}, "kind": {"manual"}, "steps": {"5"},
		"s0.type": {"wait"}, "s0.until": {"undeafened"}, "s0.to": {"trigger"}, "s0.user": {"Jane"}, "s0.channel": {"2"}, "s0.wait": {"5s"}, "s0.on_timeout": {"stop"},
		"s1.type": {"wait"}, "s1.until": {"channel_empty"}, "s1.until_was": {"channel_empty"}, "s1.to": {"member"}, "s1.user": {"Jane"}, "s1.channel": {"2"}, "s1.wait": {"5s"}, "s1.on_timeout": {"continue"},
		"s2.type": {"wait"}, "s2.until": {"in_voice"}, "s2.to": {"member"}, "s2.user": {" Jane "}, "s2.wait": {""},
		"s3.type": {"wait"}, "s3.wait": {"2y"},
		"s4.type": {"wait"}, "s4.wait": {"1,5"},
	}
	r := httptest.NewRequest("POST", "/tasks/new", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	task, bad := parseTask(r)
	if len(bad) != 0 {
		t.Fatalf("bad: %v", bad)
	}
	st := task.Steps
	if !st[0].ToTrigger || st[0].UserID != "" || st[0].ChannelID != "" || st[0].OnTimeout != tasks.OnTimeoutStop || st[0].WaitDuration() != tasks.DefaultUntilWait {
		t.Errorf("trigger member: %+v", st[0])
	}
	if st[1].ToTrigger || st[1].UserID != "" || st[1].ChannelID != "2" || st[1].OnTimeout != "" || st[1].WaitDuration() != 5*time.Second {
		t.Errorf("channel: %+v", st[1])
	}
	if st[2].UserID != "Jane" || st[2].WaitDuration() != tasks.DefaultUntilWait {
		t.Errorf("member: %+v", st[2])
	}
	if st[3].WaitInput != "2y" || st[3].WaitMS != 0 || st[4].WaitMS != 1500 {
		t.Errorf("durations: %+v %+v", st[3], st[4])
	}
	if p := task.Problems(); len(p) != 1 || p[0] != "Step 4: wait at most 24h." {
		t.Errorf("problems: %v", p)
	}

	var legacy tasks.Step
	if err := json.Unmarshal([]byte(`{"type":"wait","seconds":2}`), &legacy); err != nil || legacy.WaitDuration() != 2*time.Second || legacy.Validate() != nil {
		t.Errorf("legacy: %+v %v", legacy, err)
	}

	s := testServer(t)
	sess := &store.Session{Username: "ann", CSRFToken: "tok"}
	e := taskEditor{Task: task, StepTypes: tasks.StepTypes, Kinds: tasks.TriggerKinds, Weekdays: tasks.WeekdayNames,
		Voice: []bot.Channel{{ID: "2", Name: "lounge", Voice: true}}, Labels: map[string]string{"Jane": "Jane (@jane)"}, Page: 1, Pages: 1}
	req := httptest.NewRequest("GET", "/tasks/new", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, sess))
	rec := httptest.NewRecorder()
	s.render(rec, req, http.StatusOK, "task", e)
	body := rec.Body.String()
	for _, want := range []string{`<option value="undeafened" selected>Until a member is undeafened`, `name="s0.until_was" value="undeafened"`,
		`<option value="trigger" selected>The member who started the run`, `<option value="stop" selected>then stop the run`, `name="s0.wait" value="5m"`,
		`<option value="channel_empty" selected>Until the channel is empty`, `<option value="2" selected>lounge`, `name="s2.user" value="Jane (@jane)"`,
		`name="s3.wait" value="2y"`, `name="s4.wait" value="1.5s"`, `placeholder="500ms, 1.5s, 5m"`, "e.g. 500ms, 1.5s, 5m, 2h"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, " style=") {
		t.Error("inline style attribute, blocked by the CSP")
	}
}
