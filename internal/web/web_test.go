package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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
		Steps: []tasks.Step{{Type: tasks.Join, ChannelID: "2"}, {Type: tasks.Play, MediaIDs: []int64{5}, Volume: 80}, {Type: tasks.Message, ChannelID: "4", Text: "Adhan at {time}"},
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
		{"login", nil, []string{"Log in with Cloudron"}},
		{"error", nil, []string{"Something went wrong"}},
		{"dashboard", dashboardData{Upcoming: []upcoming{{At: now.Add(time.Hour), Task: adhan, Guild: "PTS", What: "Join prayer"}}, Runs: runs, Runs24h: 2, Failed24h: 1,
			Playing: []playingView{{P: bot.Playing{GuildID: "1", Name: "muezzin.ogg", Channel: "prayer", Started: now, Duration: time.Minute}, CSRF: "tok", Next: "/"}}},
			[]string{"Adhan", "Missing Access", `action="/servers/1/stop"`, "muezzin.ogg", "of 2 runs"}},
		{"servers", serversData{Guilds: []bot.Guild{{ID: "1", Name: "PTS Server", MemberCount: 12, Voice: 2, Text: 3, Problems: 1, Missing: []string{"Change Nickname"}}}, InviteURL: "https://discord.com/oauth2/authorize?client_id=1"},
			[]string{`href="/servers/1"`, ">PS<", "1 channel without access", "client_id=1", "lacks Change Nickname"}},
		{"server", serverData{Guild: bot.Guild{ID: "1", Name: "PTS", Missing: []string{"Speak", "Change Nickname"}}, Invite: "https://discord.com/oauth2/authorize?client_id=1", Tab: "channels", Voice: []bot.Channel{{ID: "2", Name: "prayer", Voice: true, Usable: true, HasBot: true, InVoice: []string{"Amira"}}},
			Text: []bot.Channel{{ID: "4", Name: "rules", Problem: "The bot needs View Channel and Send Messages here."}}, Sounds: []store.Media{sound}},
			[]string{`id="sheet-play-2"`, "Bot and Amira connected", "read only", `value="5"`, "The bot lacks these server permissions: Speak, Change Nickname.", "client_id=1"}},
		{"tasks", tasksData{Rows: []taskRow{{Task: adhan, Guild: "PTS", What: "Join prayer", Next: &now, Last: &runs[0]}}, Counts: map[string]int{"": 1, "active": 1}},
			[]string{"Daily 05:00, 16:35", `action="/tasks/3/pause"`, "Active 1"}},
		{"task", taskEditor{Task: adhan, StepTypes: tasks.StepTypes, Kinds: tasks.TriggerKinds, Weekdays: tasks.WeekdayNames, TimesText: "05:00 16:35",
			Voice: []bot.Channel{{ID: "2", Name: "prayer", Voice: true}}, Text: []bot.Channel{{ID: "44", Name: "general"}}, Labels: map[string]string{"9": "Amira (@amira)"},
			Sounds: []store.Media{sound}, Messages: []messages.Message{msg}, Next: adhan.Trigger.Next(now, 3), Runs: runs, RunCount: 2, Page: 1, Pages: 1},
			[]string{`name="s0.target"`, `value="2" selected`, `name="s0.follow" value=""`, `data-members="1"`, `<input type="hidden" name="s1.media" value="5">`, `data-preview-media="5"`, "Total 3:45 / 10:00", `name="s1.volume" value="80"`, `value="add:wait"`, `value="daily" checked`, "Unknown channel 4", `name="s3.user" value="Amira (@amira)"`, `name="s4.wait" value="5s"`, `action="/tasks/3/delete"`, "As cron"}},
		{"task", taskEditor{Task: tasks.Task{Trigger: tasks.Trigger{Kind: tasks.Manual}}, New: true, StepTypes: tasks.StepTypes, Kinds: tasks.TriggerKinds, Weekdays: tasks.WeekdayNames, Problems: []string{"Give the task a name."}},
			[]string{"Create task", "Give the task a name.", `action="/tasks/new"`}},
		{"messages", messagesData{Rows: []messageRow{{Message: msg, UsedBy: []string{"Adhan"}}}}, []string{`href="/messages/7"`, "1 embed", "1 file"}},
		{"message", messageEditor{Message: msg, Media: []store.Media{sound, img}, ByID: map[int64]store.Media{5: sound, 6: img}, Embeddable: map[int64]bool{6: true},
			Names: map[string]string{"123456789012345678": "Amira"}, Limits: map[string]int{"content": 2000, "embeds": 10, "embedTotal": 6000},
			Channels: []guildChannels{{Guild: bot.Guild{Name: "PTS"}, Text: []bot.Channel{{ID: "4", Name: "general"}}}}},
			[]string{`<span class="dmention">@Amira</span>`, `fill="#112233"`, `name="e0.f0.inline" checked`, `src="/media/6/file"`, `action="/messages/7/send"`, `name="file" value="6"`}},
		{"media", mediaData{Items: []store.Media{sound, img, {ID: 8, Name: "big.mp4", Kind: store.KindVideo, SizeBytes: 30 << 20}}, Counts: map[string]int{"": 3, "sound": 1}, MaxUploadMB: 100,
			Voice: []voiceGroup{{Guild: bot.Guild{Name: "PTS"}, Voice: []bot.Channel{{ID: "2", Name: "prayer"}}}}},
			[]string{`id="sheet-play-5"`, `data-preview-media="5" data-name="muezzin.ogg"`, "over 20 MB", "used by task Adhan", "3:45", `enctype="multipart/form-data"`}},
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
		for _, w := range append(p.want, "Saved.", `name="confirm_actions" checked`, `<body data-preview-vol="100">`) {
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
		"s1.type": {"play"}, "s1.media": {"3", "5"}, "s1.order": {"random"}, "s1.volume": {"80"},
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
	if p := task.Steps[1]; !slices.Equal(p.MediaIDs, []int64{3, 5}) || p.Order != tasks.OrderRandom || p.Volume != 80 {
		t.Errorf("play: %+v", p)
	}
	applyOp(&task, "tokdel:s1.media:0")
	if p := task.Steps[1]; !slices.Equal(p.MediaIDs, []int64{5}) {
		t.Errorf("tokdel: %+v", p)
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
		"cooldown": {"1m"}, "ev_except": {"on"}, "times": {"05:00"}, "steps": {"3"}, "ev_member": {" Amira (@amira) ", "", "Amira (@amira)", "Bilal"},
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
	if !task.Slash || tr.Kind != tasks.Event || tr.Event != tasks.MessageIn || tr.ChannelID != "5" || tr.Keyword != "adhan" || tr.Cooldown != 60 || !tr.Except || len(tr.Times) != 0 || strings.Join(tr.UserIDs, "|") != "Amira (@amira)|Bilal" {
		t.Errorf("trigger: %+v slash=%v", tr, task.Slash)
	}
	if !task.Steps[0].ToTrigger || task.Steps[1].Tag != "greeting" || len(task.Steps[1].MediaIDs) != 0 || !task.Steps[2].ToTrigger || task.Steps[2].UserID != "" {
		t.Errorf("steps: %+v", task.Steps)
	}
	if p := task.Problems(); len(p) != 0 {
		t.Errorf("problems: %v", p)
	}
	applyOp(&task, "tokdel:ev_member:0")
	if strings.Join(task.Trigger.UserIDs, "|") != "Bilal" {
		t.Errorf("tokdel: %v", task.Trigger.UserIDs)
	}
	applyOp(&task, "tokdel:s1.media:0")
	if task.Steps[1].Tag != "" {
		t.Errorf("tokdel tag: %+v", task.Steps[1])
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
		`name="cooldown" value="45s"`, `<input type="hidden" name="ev_member" value="9">`, `value="tokdel:ev_member:0"`, `<span class="av" aria-hidden="true">A</span>`, "1 of 25", `name="slash" checked`, `<option value="trigger" selected>The channel of the member who started the run`,
		`<input type="hidden" name="s1.media" value="tag:greeting">`, "random sound tagged greeting", `<option value="trigger" selected>To the member who started the run`, "Runs when the event happens."} {
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

func TestPlayEditorRenders(t *testing.T) {
	s := testServer(t)
	sess := &store.Session{Username: "ann", CSRFToken: "tok"}
	sounds := []store.Media{{ID: 3, Name: "intro.ogg", Kind: store.KindSound, DurationMS: 330000, Tags: []string{"intro"}}, {ID: 5, Name: "outro.ogg", Kind: store.KindSound, DurationMS: 0}}
	task := tasks.Task{ID: 4, Name: "Jingles", GuildID: "1", Busy: tasks.BusyWait, Trigger: tasks.Trigger{Kind: tasks.Manual},
		Steps: []tasks.Step{{Type: tasks.Play, Tag: "intro", MediaIDs: []int64{3, 5, 3}, Order: tasks.OrderSequence, Volume: 100}, {Type: tasks.Play, MediaIDs: []int64{3, 3}, Order: tasks.OrderRandom, Volume: 50}}}
	e := taskEditor{Task: task, StepTypes: tasks.StepTypes, Kinds: tasks.TriggerKinds, Weekdays: tasks.WeekdayNames, Sounds: sounds, MaxPlayMinutes: 10, Page: 1, Pages: 1}
	req := httptest.NewRequest("GET", "/tasks/4", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, sess))
	rec := httptest.NewRecorder()
	s.render(rec, req, http.StatusOK, "task", e)
	body := rec.Body.String()
	for _, want := range []string{`data-tokens="s0.media" data-max="25" data-empty="No sound" data-source="/sounds" data-guild="1" data-dupes`, `<input type="hidden" name="s0.media" value="tag:intro">`,
		"random sound tagged intro <span class=\"un\">5:30</span>", `<span class="tok" title="intro.ogg" data-ms="330000"><button type="button" class="pv js-only" data-preview-media="3" data-name="intro.ogg" aria-label="Preview intro.ogg" data-tip="Preview intro.ogg" aria-pressed="false">`, `<span class="nm" data-preview-detail>intro.ogg`, `value="tokdel:s0.media:3"`,
		"outro.ogg <span class=\"un\">length unknown</span>", "4 of 25", `name="s0.find" value=""`, `<li class="step bad">`, "Total 16:30 / 10:00, some lengths unknown",
		`name="s1.order" value="random" checked`, "Total 5:30 / 10:00</span>", `name="s0.order" value="sequence" checked`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, " style=") {
		t.Error("inline style attribute, blocked by the CSP")
	}
}

func TestProfileStepEditor(t *testing.T) {
	form := url.Values{"name": {"Rename"}, "guild": {"1"}, "kind": {"manual"}, "steps": {"4"},
		"s0.type": {"profile"}, "s0.nick_mode": {"set"}, "s0.nick": {" DJ {member_name} "}, "s0.avatar": {"7"},
		"s1.type": {"profile"}, "s1.nick_mode": {"reset"}, "s1.nick": {"ignored"}, "s1.avatar": {"reset"},
		"s2.type": {"profile"}, "s2.nick_mode": {""}, "s2.nick": {"ignored"}, "s2.avatar": {"9"},
		"s3.type": {"profile"}, "s3.nick_mode": {"set"}, "s3.nick": {" "},
	}
	r := httptest.NewRequest("POST", "/tasks/new", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	task, bad := parseTask(r)
	if len(bad) != 1 || bad[0] != "Step 4: enter the nickname." {
		t.Errorf("bad: %v", bad)
	}
	st := task.Steps
	if st[0].Nick != "DJ {member_name}" || st[0].AvatarMediaID != 7 || st[0].ResetNick || st[0].ResetAvatar {
		t.Errorf("set: %+v", st[0])
	}
	if !st[1].ResetNick || !st[1].ResetAvatar || st[1].Nick != "" || st[1].AvatarMediaID != 0 {
		t.Errorf("reset: %+v", st[1])
	}
	if st[2].Nick != "" || st[2].AvatarMediaID != 9 {
		t.Errorf("avatar only: %+v", st[2])
	}
	if p := task.Problems(); len(p) != 1 || p[0] != "Step 4: pick a nickname or an avatar to change." {
		t.Errorf("problems: %v", p)
	}

	images := []store.Media{{ID: 7, Name: "cat.png", Kind: store.KindImage, ContentType: "image/png", SizeBytes: 1000},
		{ID: 8, Name: "huge.png", Kind: store.KindImage, ContentType: "image/png", SizeBytes: tasks.MaxAvatarBytes + 1}}
	task.Steps[3] = tasks.Step{Type: tasks.Profile, AvatarMediaID: 8}
	want := []string{"Step 3: the avatar image was deleted from the media library, pick another.", "Step 4: huge.png has 8.0 MB, an avatar may have at most 8 MB."}
	if got := avatarProblems(task, images); !slices.Equal(got, want) {
		t.Errorf("avatar problems %q", got)
	}

	s := testServer(t)
	sess := &store.Session{Username: "ann", CSRFToken: "tok"}
	e := taskEditor{Task: task, StepTypes: tasks.StepTypes, Kinds: tasks.TriggerKinds, Weekdays: tasks.WeekdayNames, Images: images, Page: 1, Pages: 1}
	req := httptest.NewRequest("GET", "/tasks/new", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, sess))
	rec := httptest.NewRecorder()
	s.render(rec, req, http.StatusOK, "task", e)
	body := rec.Body.String()
	for _, want := range []string{`<option value="set" selected>Set the nickname to`, `name="s0.nick" value="DJ {member_name}"`, `<option value="7" selected>cat.png`,
		`<option value="reset" selected>Reset to the bot's name`, `<option value="reset" selected>Reset to the bot's avatar`,
		`<option value="9" selected>Deleted image`, `<option value="8" selected>huge.png`, "huge.png has 8.0 MB, an avatar may have at most 8 MB. Pick another image.",
		`<li class="step bad">`, "Change bot profile", "Nickname and avatar in this server", `value="add:profile"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, `<option value="8">huge.png`) {
		t.Error("an image Discord refuses is offered")
	}
	if strings.Contains(body, " style=") {
		t.Error("inline style attribute, blocked by the CSP")
	}
	if strings.Contains(body, "\u2014") {
		t.Error("em dash in the editor")
	}
}

func TestPlayProblems(t *testing.T) {
	sounds := []store.Media{{ID: 1, Name: "a", DurationMS: 4 * 60000, Tags: []string{"x"}}, {ID: 2, Name: "b", DurationMS: 7 * 60000, Tags: []string{"x"}}, {ID: 3, Name: "c"}}
	play := func(order, tag string, ids ...int64) tasks.Task {
		return tasks.Task{Steps: []tasks.Step{{Type: tasks.Leave}, {Type: tasks.Play, MediaIDs: ids, Tag: tag, Order: order, Volume: 100}}}
	}
	for _, c := range []struct {
		task tasks.Task
		want string
	}{
		{play("", "", 1, 2), "Step 2: sounds add up to 11:00, longest playback is 10 min."},
		{play(tasks.OrderRandom, "", 1, 2, 3), ""},
		{play("", "", 1, 1, 3), ""},
		{play("", "x", 1), "Step 2: sounds add up to 11:00, longest playback is 10 min."},
		{play(tasks.OrderRandom, "x", 3), ""},
		{play("", "", 9), "Step 2: a picked sound was deleted from the media library, remove it."},
	} {
		got := strings.Join(playProblems(c.task, sounds, 10), " ")
		if got != c.want {
			t.Errorf("%+v: got %q, want %q", c.task.Steps[1], got, c.want)
		}
	}
	if ms, unknown := playLength(play(tasks.OrderRandom, "", 1, 3).Steps[1], sounds); ms != 4*60000 || !unknown {
		t.Errorf("random length %d %v", ms, unknown)
	}
	if ms, unknown := playLength(play("", "", 1, 2).Steps[1], sounds); ms != 11*60000 || unknown {
		t.Errorf("sequence length %d %v", ms, unknown)
	}
}

func TestConfirmAndPreviewMarkup(t *testing.T) {
	s := testServer(t)
	sess := &store.Session{Username: "ann", CSRFToken: "tok"}
	render := func(page string, data any, prefs store.Prefs) string {
		var b strings.Builder
		if err := s.pages[page].Execute(&b, pageData{Session: sess, Prefs: prefs, Data: data, TrashDays: 30}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(), " style=") {
			t.Errorf("%s: inline style attribute, blocked by the CSP", page)
		}
		return b.String()
	}
	task := tasks.Task{ID: 3, Name: "Adhan", GuildID: "1", Trigger: tasks.Trigger{Kind: tasks.Manual}}
	editor := taskEditor{Task: task, StepTypes: tasks.StepTypes, Kinds: tasks.TriggerKinds, Weekdays: tasks.WeekdayNames, Page: 1, Pages: 1}

	body := render("task", editor, store.DefaultPrefs)
	if !strings.Contains(body, `<body data-preview-vol="100">`) || !strings.Contains(body, `name="confirm_actions" checked`) {
		t.Error("confirm actions is not on by default")
	}
	for _, want := range []string{`<button type="submit" class="ib danger" aria-label="Delete task" data-tip="Delete task" data-confirm-desc="Moves the task and its run history to the Trash.">`,
		`<span class="lbl">Delete task</span>`, "Deleting for good and emptying the Trash always ask."} {
		if !strings.Contains(body, want) {
			t.Errorf("task: missing %q", want)
		}
	}
	for _, gone := range []string{`name="confirm"`, "needs-confirm", "data-confirm-click", "with-label needs"} {
		if strings.Contains(body, gone) {
			t.Errorf("task: still has %q", gone)
		}
	}

	body = render("task", editor, store.Prefs{ShowLabels: true})
	if !strings.Contains(body, `<body data-preview-vol="0" data-confirm="off">`) || strings.Contains(body, `name="confirm_actions" checked`) || !strings.Contains(body, `<html lang="en" class="labels">`) {
		t.Error("confirm actions off is not rendered")
	}

	msg := messageEditor{Message: messages.Message{ID: 7, Name: "Friday"}, Limits: map[string]int{"content": 2000, "embeds": 10, "embedTotal": 6000}}
	body = render("message", msg, store.DefaultPrefs)
	if !strings.Contains(body, `data-confirm-desc="Moves the message to the Trash."`) || strings.Contains(body, `name="confirm"`) {
		t.Error("message: delete button without data-confirm-desc or with a name field")
	}

	sound := store.Media{ID: 5, Name: "muezzin.ogg", Kind: store.KindSound, DurationMS: 225000}
	video := store.Media{ID: 8, Name: "clip.mp4", Kind: store.KindVideo}
	body = render("media", mediaData{Items: []store.Media{sound, video}, Counts: map[string]int{}}, store.DefaultPrefs)
	for _, want := range []string{`<button type="button" class="preview-tile js-only" data-preview-media="5" data-name="muezzin.ogg" aria-label="Preview muezzin.ogg" data-tip="Preview muezzin.ogg" aria-pressed="false">`,
		`<span class="row-sub" data-preview-detail>3:45`, `data-confirm-desc="Moves muezzin.ogg to the Trash."`} {
		if !strings.Contains(body, want) {
			t.Errorf("media: missing %q", want)
		}
	}
	if strings.Contains(body, "<audio") || strings.Contains(body, `data-preview-media="8"`) {
		t.Error("media: audio element or preview of a video")
	}
}

func TestTrashRender(t *testing.T) {
	s := testServer(t)
	now := time.Now()
	daily := tasks.Task{ID: 3, Name: "Adhan", Trigger: tasks.Trigger{Kind: tasks.Daily, Times: []string{"05:00"}, Timezone: "UTC"}, Steps: []tasks.Step{{Type: tasks.Leave}}}
	msg := messages.Message{ID: 7, Name: "Friday", Files: []int64{6}}
	sound := store.Media{ID: 5, Name: "intro.ogg", Kind: store.KindSound, SizeBytes: 2048, DurationMS: 3000}
	d := trashData{Days: 30, Paging: paging{Page: 1, path: "/trash"}, Counts: map[string]int{"": 3, "task": 1, "message": 1, "media": 1}, Rows: []trashRow{
		{TrashItem: store.TrashItem{Kind: store.TrashTask, ID: 3, Name: "Adhan", DeletedAt: now, DeletedBy: "ann", Task: &daily}, DaysLeft: 30},
		{TrashItem: store.TrashItem{Kind: store.TrashMessage, ID: 7, Name: "Friday", DeletedAt: now.Add(-3 * time.Hour), DeletedBy: "bob", Message: &msg}, DaysLeft: 1},
		{TrashItem: store.TrashItem{Kind: store.TrashMedia, ID: 5, Name: "intro.ogg", DeletedAt: now.Add(-30 * 24 * time.Hour), DeletedBy: "ann", Media: &sound}, RestoreAs: "intro (restored).ogg"},
	}}
	for _, sess := range []*store.Session{
		{Username: "ann", DisplayName: "Ann Admin", IsAdmin: true, CSRFToken: "tok"},
		{Username: "bob", DisplayName: "Bob", CSRFToken: "tok"},
	} {
		req := httptest.NewRequest("GET", "/trash?done=trashed&undo=task.3", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, sess))
		rec := httptest.NewRecorder()
		s.render(rec, req, http.StatusOK, "trash", d)
		body := rec.Body.String()
		for _, w := range []string{"Moved to the Trash.", `action="/trash/task/3/restore"`, `action="/trash/media/5/restore"`, "stay here for 30 days",
			"Daily 05:00 · 1 step", "1 file", "0:03 · 2.0 KB", "removed in 30 days", `chip warn">removed tomorrow`, "removed today", "Deleted by bob 3 h ago",
			`data-preview-media="5"`, "comes back as intro (restored).ogg", "It stays paused until you resume it.", `href="/trash" aria-current="page"`, "All 3"} {
			if !strings.Contains(body, w) {
				t.Errorf("%s: missing %q", sess.Username, w)
			}
		}
		purge := strings.Contains(body, `/purge"`) || strings.Contains(body, `action="/trash/empty"`)
		if purge != sess.IsAdmin {
			t.Errorf("%s: purge buttons shown %v", sess.Username, purge)
		}
		if strings.Contains(body, " style=") {
			t.Errorf("%s: inline style attribute, blocked by the CSP", sess.Username)
		}
	}

	req := httptest.NewRequest("GET", "/trash?kind=media", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, &store.Session{Username: "bob", CSRFToken: "tok"}))
	rec := httptest.NewRecorder()
	s.render(rec, req, http.StatusOK, "trash", trashData{Kind: "media", Paging: paging{Page: 1}, Counts: map[string]int{}})
	if body := rec.Body.String(); !strings.Contains(body, "Nothing of this kind in the trash.") || !strings.Contains(body, "removes items right away") {
		t.Errorf("empty trash:\n%s", body)
	}
}

func TestTrashHelpers(t *testing.T) {
	for in, ok := range map[string]bool{"task.3": true, "media.12": true, "message.1": true, "task.0": false, "user.3": false, "task": false, "": false} {
		if got := parseTrashRef(in); (got != nil) != ok {
			t.Errorf("parseTrashRef(%q) = %v", in, got)
		}
	}
	for name, want := range map[string][2]string{
		"intro.ogg":  {"intro (restored).ogg", "intro (restored 2).ogg"},
		"README":     {"README (restored)", "README (restored 2)"},
		".hidden":    {".hidden (restored)", ".hidden (restored 2)"},
		"a.b (x).gz": {"a.b (x) (restored).gz", "a.b (x) (restored 2).gz"},
	} {
		if got := [2]string{store.RestoredName(name, 1), store.RestoredName(name, 2)}; got != want || store.RestoredName(name, 0) != name {
			t.Errorf("RestoredName(%q) = %v", name, got)
		}
	}
	SetDisplayTimezone("UTC")
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		deleted time.Time
		want    int
	}{{now, 30}, {now.Add(-29 * 24 * time.Hour), 1}, {now.Add(-30*24*time.Hour + time.Hour), 0}, {now.Add(-40 * 24 * time.Hour), 0}} {
		if got := daysLeft(c.deleted, 30, now); got != c.want {
			t.Errorf("daysLeft(%v) = %d, want %d", c.deleted, got, c.want)
		}
	}
}

func TestPaging(t *testing.T) {
	p := newPaging(httptest.NewRequest("GET", "/media?kind=sound&q=+be%26ep+&page=2&undo=media.3", nil))
	p.Keep("kind", "sound")
	p.Keep("tag", "")
	if p.Page != 2 || p.Query != "be&ep" || p.Offset() != perPage || p.Limit() != perPage+1 {
		t.Fatalf("parsed: %+v", p)
	}
	for _, c := range [][2]string{
		{p.Prev(), "/media?kind=sound&q=be%26ep"},
		{p.Next(), "/media?kind=sound&page=3&q=be%26ep"},
		{p.With("tag", "fun"), "/media?q=be%26ep&tag=fun"},
		{p.With("", ""), "/media?q=be%26ep"},
		{paging{path: "/x"}.Prev(), "/x"},
	} {
		if got, want := c[0], c[1]; got != want {
			t.Errorf("link %q, want %q", got, want)
		}
	}
	if !p.Matches("x", "BE&EP.ogg") || p.Matches("beep") || !(paging{}).Matches("anything") {
		t.Error("Matches")
	}

	list := make([]int, 2*perPage+3)
	for i := range list {
		list[i] = i
	}
	if page := window(&p, list); len(page) != perPage || page[0] != perPage || !p.More {
		t.Errorf("window page 2: %v more=%v", page, p.More)
	}
	p.Page = 3
	if page := window(&p, list); len(page) != 3 || p.More {
		t.Errorf("window page 3: %v more=%v", page, p.More)
	}
	p.Page = 9
	if page := window(&p, list); len(page) != 0 || p.More {
		t.Errorf("window past the end: %v", page)
	}
	if page := cut(&p, list[:perPage+1]); len(page) != perPage || !p.More {
		t.Errorf("cut: %d more=%v", len(page), p.More)
	}
	if page := cut(&p, list[:3]); len(page) != 3 || p.More {
		t.Errorf("cut short: %d more=%v", len(page), p.More)
	}
}

func TestListSearchAndPager(t *testing.T) {
	s := testServer(t)
	sess := &store.Session{Username: "ann", IsAdmin: true, CSRFToken: "tok"}
	paged := func(target string, keep ...string) paging {
		r := httptest.NewRequest("GET", target, nil)
		p := newPaging(r)
		for i := 0; i < len(keep); i += 2 {
			p.Keep(keep[i], keep[i+1])
		}
		return p
	}
	sound := store.Media{ID: 5, Name: "beep.ogg", Kind: store.KindSound, Tags: []string{"fun"}}
	task := tasks.Task{ID: 3, Name: "Adhan", GuildID: "1", Trigger: tasks.Trigger{Kind: tasks.Manual}}
	more := func(p paging) paging { p.More = true; return p }
	cases := []struct {
		name, target string
		data         any
		want         []string
	}{
		{"media", "/media?kind=sound&q=beep&page=2", mediaData{Kind: "sound", Items: []store.Media{sound}, Counts: map[string]int{"": 1}, Tags: []string{"fun"},
			Paging: more(paged("/media?kind=sound&q=beep&page=2", "kind", "sound"))},
			[]string{`<form class="search" method="get" action="/media">`, `<input type="hidden" name="kind" value="sound">`, `type="search" name="q" value="beep"`, `aria-label="Search media"`,
				`href="/media?kind=sound&amp;q=beep" rel="prev"`, `href="/media?kind=sound&amp;page=3&amp;q=beep" rel="next"`, "Page 2",
				`href="/media?kind=video&amp;q=beep"`, `href="/media?q=beep&amp;tag=fun"`, `href="/media?q=beep"`}},
		{"media", "/media?q=nope", mediaData{Counts: map[string]int{}, Paging: paged("/media?q=nope")}, []string{`Nothing matches "nope".`}},
		{"messages", "/messages?page=2", messagesData{Paging: paged("/messages?page=2")}, []string{`aria-label="Search messages"`, `href="/messages" rel="prev"`, "No saved messages yet."}},
		{"messages", "/messages?q=fri", messagesData{Paging: paged("/messages?q=fri")}, []string{`Nothing matches "fri".`}},
		{"tasks", "/tasks?filter=manual", tasksData{Filter: "manual", Counts: map[string]int{}, Rows: []taskRow{{Task: task}}, Paging: more(paged("/tasks?filter=manual", "filter", "manual"))},
			[]string{`<input type="hidden" name="filter" value="manual">`, `href="/tasks?filter=manual&amp;page=2" rel="next"`, `href="/tasks?filter=paused"`, `>Next<`}},
		{"tasks", "/tasks?q=zz", tasksData{Counts: map[string]int{}, Paging: paged("/tasks?q=zz")}, []string{`Nothing matches "zz".`}},
		{"trash", "/trash?kind=task&q=ann&page=2", trashData{Kind: "task", Counts: map[string]int{}, Paging: func() paging {
			p := paged("/trash?kind=task&q=ann&page=2", "kind", "task")
			p.Dated = true
			return p
		}()},
			[]string{`aria-label="Search the trash"`, `<input type="hidden" name="kind" value="task">`, `href="/trash?kind=task&amp;q=ann" rel="prev"`, `>Newer<`, `Nothing matches "ann".`}},
		{"servers", "/servers?q=pts", serversData{Guilds: []bot.Guild{{ID: "1", Name: "PTS"}}, Paging: more(paged("/servers?q=pts"))},
			[]string{`aria-label="Search servers"`, `href="/servers?page=2&amp;q=pts" rel="next"`}},
		{"server", "/servers/1?tab=tasks&page=2", serverData{Guild: bot.Guild{ID: "1", Name: "PTS"}, Tab: "tasks", Tasks: []tasks.Task{task}, Paging: paged("/servers/1?tab=tasks&page=2", "tab", "tasks")},
			[]string{`<input type="hidden" name="tab" value="tasks">`, `href="/servers/1?tab=tasks" rel="prev"`, `href="/tasks/3"`}},
		{"dashboard", "/", dashboardData{Upcoming: []upcoming{{Task: task}}, MoreSoon: true}, []string{`href="/tasks?filter=active">Show all active tasks`}},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", c.target, nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, sess))
		rec := httptest.NewRecorder()
		s.render(rec, req, http.StatusOK, c.name, c.data)
		body := rec.Body.String()
		for _, w := range c.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s %s: missing %q", c.name, c.target, w)
			}
		}
	}

	req := httptest.NewRequest("GET", "/media", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, sess))
	rec := httptest.NewRecorder()
	s.render(rec, req, http.StatusOK, "media", mediaData{Items: []store.Media{sound}, Counts: map[string]int{}, Paging: paged("/media")})
	if strings.Contains(rec.Body.String(), `class="pager`) {
		t.Error("media: pager on a single page")
	}
}
