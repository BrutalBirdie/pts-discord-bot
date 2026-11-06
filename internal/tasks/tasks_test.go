package tasks

import (
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
		Steps: []Step{{Type: Join, ChannelID: "2"}, {Type: Play, MediaID: 1, Volume: 100}, {Type: Leave}}}
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
