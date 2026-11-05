package web

import (
	"context"
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
			[]string{`name="s0.target"`, `value="2" selected`, `name="s0.follow" value=""`, `data-members="1"`, `value="5" selected`, `name="s1.volume" value="80"`, `value="add:wait"`, `value="daily" checked`, "Unknown channel 4", `name="s3.user" value="Amira (@amira)"`, `name="s4.seconds" value="5"`, `action="/tasks/3/delete"`, "As cron"}},
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
		"s3.type": {"wait"}, "s3.seconds": {"2"},
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
	if len(task.Steps) != 4 || task.Steps[0].FollowUserID != "Amira (@amira)" || task.Steps[2].UserID != "<@123>" || task.Steps[2].ChannelID != "" {
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
