package tasks

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func TestParseTimes(t *testing.T) {
	got, err := ParseTimes("18:15, 5:00 11.50  05:00")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"05:00", "11:50", "18:15"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, err := ParseTimes("25:00"); err == nil {
		t.Error("25:00 accepted")
	}
}

func TestDailyNextFollowsTimezone(t *testing.T) {
	tr := Trigger{Kind: Daily, Times: []string{"05:00", "16:35"}, Timezone: "Europe/Berlin"}
	berlin, _ := time.LoadLocation("Europe/Berlin")
	// the day before the switch to summer time
	from := time.Date(2026, 3, 28, 17, 0, 0, 0, berlin)
	got := tr.Next(from, 3)
	want := []time.Time{
		time.Date(2026, 3, 29, 5, 0, 0, 0, berlin),
		time.Date(2026, 3, 29, 16, 35, 0, 0, berlin),
		time.Date(2026, 3, 30, 5, 0, 0, 0, berlin),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("run %d: got %v, want %v", i, got[i], want[i])
		}
	}
	if got[0].UTC().Hour() != 3 {
		t.Errorf("05:00 Berlin summer time should be 03:00 UTC, got %v", got[0].UTC())
	}
}

func TestWeeklyAndCron(t *testing.T) {
	from := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) // Thursday
	weekly := Trigger{Kind: Weekly, Times: []string{"12:00"}, Weekdays: []int{5}, Timezone: "UTC"}
	if got := weekly.Next(from, 1); len(got) != 1 || got[0].Weekday() != time.Friday {
		t.Errorf("weekly: got %v", got)
	}
	c := Trigger{Kind: Cron, Cron: []string{"*/15 9-17 * * 1-5", "0 3 * * 0"}, Timezone: "UTC"}
	if got := c.Next(from, 1); len(got) != 1 || !got[0].Equal(from.Add(15*time.Minute)) {
		t.Errorf("cron: got %v", got)
	}
	bad := Trigger{Kind: Cron, Cron: []string{"CRON_TZ=UTC 0 3 * * *"}}
	if bad.Validate() == nil {
		t.Error("CRON_TZ accepted")
	}
	if (Trigger{Kind: Cron, Cron: []string{"61 * * * *"}}).Validate() == nil {
		t.Error("minute 61 accepted")
	}
}

func TestOnceRunsOnce(t *testing.T) {
	tr := Trigger{Kind: Once, OnceAt: "2026-11-12T08:00", Timezone: "Europe/Berlin"}
	from := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	got := tr.Next(from, 3)
	if len(got) != 1 || got[0].UTC().Hour() != 7 {
		t.Errorf("got %v", got)
	}
	if tr.Summary() != "Once, 12 Nov 2026 08:00" {
		t.Errorf("summary %q", tr.Summary())
	}
}

func TestManualHasNoSchedule(t *testing.T) {
	s, err := Trigger{Kind: Manual}.Schedule()
	if err != nil || s != nil {
		t.Errorf("got %v %v", s, err)
	}
}

func TestProblems(t *testing.T) {
	task := Task{Name: "Adhan", GuildID: "1", Trigger: Trigger{Kind: Daily, Times: []string{"05:00"}, Timezone: "Europe/Berlin"},
		Steps: []Step{{Type: Join, ChannelID: "2"}, {Type: Play, MediaIDs: []int64{1}, Volume: 100}, {Type: Leave}}}
	if p := task.Problems(); len(p) != 0 {
		t.Errorf("valid task: %v", p)
	}
	task.Steps = append(task.Steps, Step{Type: Play, Volume: 100}, Step{Type: Wait})
	if p := task.Problems(); len(p) != 2 {
		t.Errorf("want 2 problems, got %v", p)
	}
	if !task.UsesVoice() {
		t.Error("UsesVoice false")
	}
}

func TestEventTrigger(t *testing.T) {
	tr := Trigger{Kind: Event, Event: MessageIn, Keyword: " Adhan ", ChannelID: "5"}
	if err := tr.Validate(); err != nil {
		t.Fatal(err)
	}
	if !tr.Matches(MessageIn, "5", "7", "when is ADHAN today?") || tr.Matches(MessageIn, "6", "7", "adhan") || tr.Matches(MessageIn, "5", "7", "hello") || tr.Matches(VoiceJoin, "5", "7", "adhan") {
		t.Error("message matching")
	}
	if (Trigger{Kind: Event, Event: VoiceJoin}).Matches(VoiceJoin, "9", "7", "") != true {
		t.Error("any channel")
	}
	only := Trigger{Kind: Event, Event: VoiceJoin, UserIDs: []string{"7", "8"}}
	if !only.Matches(VoiceJoin, "9", "8", "") || only.Matches(VoiceJoin, "9", "6", "") {
		t.Error("member filter")
	}
	if got := only.Summary(); got != "When a member joins a voice channel, only 2 members" {
		t.Errorf("summary %q", got)
	}
	except := Trigger{Kind: Event, Event: VoiceJoin, UserIDs: []string{"7"}, Except: true}
	if except.Matches(VoiceJoin, "9", "7", "") || !except.Matches(VoiceJoin, "9", "6", "") {
		t.Error("except filter")
	}
	if got := except.Summary(); got != "When a member joins a voice channel, all members except 1" {
		t.Errorf("summary %q", got)
	}
	if (Trigger{Kind: Event, Event: VoiceJoin, UserIDs: make([]string, MaxEventMembers+1)}).Validate() == nil {
		t.Error("too many members accepted")
	}
	if (Trigger{Kind: Event, Event: MessageIn}).Validate() == nil || (Trigger{Kind: Event}).Validate() == nil {
		t.Error("missing keyword or event accepted")
	}
	if s, err := tr.Schedule(); s != nil || err != nil {
		t.Error("events have no schedule")
	}
	if got := (Trigger{Kind: Event, Event: VoiceJoin}).Summary(); got != "When a member joins a voice channel" {
		t.Errorf("summary %q", got)
	}
	if (Step{Type: DM, ToTrigger: true, Text: "hi"}).Validate() != nil || (Step{Type: Play, Tag: "greeting", Volume: 100}).Validate() != nil {
		t.Error("trigger member or tag steps rejected")
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"1.5":      1500 * time.Millisecond,
		"300":      5 * time.Minute,
		"1,5":      1500 * time.Millisecond,
		"500ms":    500 * time.Millisecond,
		"1m30s":    90 * time.Second,
		"1.5h":     90 * time.Minute,
		" 2 H ":    2 * time.Hour,
		"1d":       24 * time.Hour,
		"5min":     5 * time.Minute,
		"0.1s":     100 * time.Millisecond,
		"1h1m1.5s": time.Hour + time.Minute + 1500*time.Millisecond,
	} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("%q: got %v %v, want %v", in, got, err, want)
		}
	}
	for in, msg := range map[string]string{
		"1y":    "wait at most 24h",
		"25h":   "wait at most 24h",
		"1d1s":  "wait at most 24h",
		"50ms":  "wait at least 100ms",
		"0":     "wait at least 100ms",
		"":      "enter how long to wait, e.g. 1.5s or 5m",
		"-5s":   `"-5s" is not a duration, use e.g. 500ms, 1.5s, 5m or 2h`,
		"1h30":  `"1h30" is not a duration, use e.g. 500ms, 1.5s, 5m or 2h`,
		"1.2.3": `"1.2.3" is not a duration, use e.g. 500ms, 1.5s, 5m or 2h`,
		"5x":    `"5x" is not a duration, use e.g. 500ms, 1.5s, 5m or 2h`,
		"1e3s":  `"1e3s" is not a duration, use e.g. 500ms, 1.5s, 5m or 2h`,
		"s":     `"s" is not a duration, use e.g. 500ms, 1.5s, 5m or 2h`,
	} {
		if _, err := ParseDuration(in); err == nil || err.Error() != msg {
			t.Errorf("%q: got %v, want %q", in, err, msg)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		1500 * time.Millisecond:         "1.5s",
		500 * time.Millisecond:          "500ms",
		5 * time.Minute:                 "5m",
		90 * time.Minute:                "1h30m",
		24 * time.Hour:                  "24h",
		61001 * time.Millisecond:        "1m1.001s",
		time.Hour + 5*time.Second:       "1h5s",
		100 * time.Millisecond:          "100ms",
		2*time.Second + time.Nanosecond: "2s",
	} {
		got := FormatDuration(d)
		if got != want {
			t.Errorf("%v: got %q, want %q", d, got, want)
		}
		if back, err := ParseDuration(got); err != nil || back != d.Round(time.Millisecond) {
			t.Errorf("%q does not parse back: %v %v", got, back, err)
		}
	}
}

func TestWaitStep(t *testing.T) {
	legacy := Step{Type: Wait, Seconds: 2}
	if legacy.WaitDuration() != 2*time.Second || legacy.WaitValue() != "2s" || legacy.Validate() != nil {
		t.Errorf("legacy seconds: %v %q %v", legacy.WaitDuration(), legacy.WaitValue(), legacy.Validate())
	}
	for name, c := range map[string]struct {
		s   Step
		err string
	}{
		"time":            {Step{Type: Wait, WaitMS: 1500}, ""},
		"no time":         {Step{Type: Wait}, "enter how long to wait, e.g. 1.5s or 5m"},
		"too long":        {Step{Type: Wait, WaitMS: (25 * time.Hour).Milliseconds()}, "wait at most 24h"},
		"too short":       {Step{Type: Wait, WaitMS: 50}, "wait at least 100ms"},
		"unparsed input":  {Step{Type: Wait, WaitInput: "2y"}, "wait at most 24h"},
		"member":          {Step{Type: Wait, WaitMS: 300000, Until: UntilUndeafened, UserID: "7"}, ""},
		"trigger member":  {Step{Type: Wait, WaitMS: 300000, Until: UntilUnmuted, ToTrigger: true, OnTimeout: OnTimeoutStop}, ""},
		"no member":       {Step{Type: Wait, WaitMS: 300000, Until: UntilInVoice, ChannelID: "2"}, "pick the member to wait for"},
		"left any":        {Step{Type: Wait, WaitMS: 300000, Until: UntilLeftVoice, UserID: "7"}, ""},
		"channel":         {Step{Type: Wait, WaitMS: 300000, Until: UntilEmpty, ChannelID: "2"}, ""},
		"no channel":      {Step{Type: Wait, WaitMS: 300000, Until: UntilOccupied, UserID: "7"}, "pick the voice channel to watch"},
		"unknown until":   {Step{Type: Wait, WaitMS: 300000, Until: "speaking"}, `unknown wait condition "speaking"`},
		"unknown timeout": {Step{Type: Wait, WaitMS: 300000, Until: UntilEmpty, ChannelID: "2", OnTimeout: "retry"}, `unknown timeout action "retry"`},
	} {
		got := ""
		if err := c.s.Validate(); err != nil {
			got = err.Error()
		}
		if got != c.err {
			t.Errorf("%s: got %q, want %q", name, got, c.err)
		}
	}
	s := Step{Type: Wait, Until: UntilUndeafened, UserID: "7"}
	if got := s.UntilText("Jane", ""); got != "until Jane is undeafened" {
		t.Errorf("until text %q", got)
	}
	s = Step{Type: Wait, Until: UntilLeftVoice, ToTrigger: true, ChannelID: "2"}
	if got := s.UntilText("", "Lobby"); got != "until the member who started the run has left Lobby" {
		t.Errorf("until text %q", got)
	}
}

func TestPlayStep(t *testing.T) {
	var steps []Step
	if err := json.Unmarshal([]byte(`[{"type":"play","media_id":5,"volume":80},{"type":"play","tag":"intro","volume":100},{"type":"play","media_ids":[3,3,4],"order":"random","volume":100}]`), &steps); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(steps[0].MediaIDs, []int64{5}) || steps[0].MediaID != 0 || steps[1].Tag != "intro" || steps[2].Order != OrderRandom {
		t.Errorf("loaded %+v", steps)
	}
	if got := steps[1].Sounds(); len(got) != 1 || got[0].Tag != "intro" {
		t.Errorf("tag entries %+v", got)
	}
	if got := steps[2].Sounds(); len(got) != 3 || got[1].MediaID != 3 {
		t.Errorf("entries %+v", got)
	}
	for i, s := range steps {
		if err := s.Validate(); err != nil {
			t.Errorf("step %d: %v", i, err)
		}
	}
	b, _ := json.Marshal(steps[0])
	if string(b) != `{"type":"play","media_ids":[5],"volume":80}` {
		t.Errorf("saved as %s", b)
	}
	if (Step{Type: Play, Volume: 100}).Validate() == nil || (Step{Type: Play, Volume: 100, MediaIDs: make([]int64, MaxSounds+1)}).Validate() == nil ||
		(Step{Type: Play, Volume: 100, MediaIDs: []int64{1}, Order: "shuffle"}).Validate() == nil {
		t.Error("invalid play steps accepted")
	}
}

func TestParseCooldown(t *testing.T) {
	for in, want := range map[string]int{"30": 30, "0": 0, "2m": 120, "1h": 3600, "1m30s": 90, "1,5m": 90, "24h": 86400, "0.4s": 0} {
		if got, err := ParseCooldown(in); err != nil || got != want {
			t.Errorf("%q: %d %v, want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "x", "25h", "2w", "-5"} {
		if _, err := ParseCooldown(in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}
