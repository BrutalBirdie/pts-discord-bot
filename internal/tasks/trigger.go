// Package tasks holds the task model: when a task runs (Trigger) and what it does (Step).
package tasks

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

type TriggerKind string

const (
	Daily  TriggerKind = "daily"
	Weekly TriggerKind = "weekly"
	Once   TriggerKind = "once"
	Cron   TriggerKind = "cron"
	Manual TriggerKind = "manual"
	Event  TriggerKind = "event"
)

var TriggerKinds = []TriggerKind{Daily, Weekly, Once, Cron, Event, Manual}

// Discord events a task can react to.
const (
	VoiceJoin  = "voice_join"
	VoiceLeave = "voice_leave"
	MemberJoin = "member_join"
	MessageIn  = "message"
)

var EventTypes = []string{VoiceJoin, VoiceLeave, MemberJoin, MessageIn}

func EventLabel(e string) string {
	return map[string]string{VoiceJoin: "A member joins a voice channel", VoiceLeave: "A member leaves a voice channel",
		MemberJoin: "A new member joins the server", MessageIn: "A message contains a keyword"}[e]
}

const (
	DefaultCooldown = 30
	MaxCooldown     = 86400
	MaxEventMembers = 25
)

// OnceLayout is the format of an <input type="datetime-local">.
const OnceLayout = "2006-01-02T15:04"

type Trigger struct {
	Kind     TriggerKind `json:"kind"`
	Times    []string    `json:"times,omitempty"`    // HH:MM, daily and weekly
	Weekdays []int       `json:"weekdays,omitempty"` // 0 is Sunday, as in cron
	OnceAt   string      `json:"once_at,omitempty"`  // OnceLayout in Timezone
	Cron     []string    `json:"cron,omitempty"`     // standard 5 field expressions
	Timezone string      `json:"timezone"`

	Event     string   `json:"event,omitempty"`      // one of EventTypes
	ChannelID string   `json:"channel_id,omitempty"` // event only in this channel, empty for all
	Keyword   string   `json:"keyword,omitempty"`    // message events
	UserIDs   []string `json:"user_ids,omitempty"`   // only these members start the task, empty for all
	Except    bool     `json:"except,omitempty"`     // UserIDs are the members who do not start the task
	Cooldown  int      `json:"cooldown,omitempty"`   // seconds before the same member starts the task again
}

var WeekdayNames = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// ParseTimes accepts "05:00, 11:50 14:00" and returns sorted, unique HH:MM values.
func ParseTimes(s string) ([]string, error) {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '\n' }) {
		t, err := time.Parse("15:04", f)
		if err != nil {
			if t, err = time.Parse("15.04", f); err != nil {
				return nil, fmt.Errorf("%q is not a time like 05:30", f)
			}
		}
		v := t.Format("15:04")
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (t Trigger) Location() (*time.Location, error) {
	if t.Timezone == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q", t.Timezone)
	}
	return loc, nil
}

func (t Trigger) specs() ([]string, error) {
	switch t.Kind {
	case Daily, Weekly:
		if len(t.Times) == 0 {
			return nil, errors.New("add at least one time")
		}
		dow := "*"
		if t.Kind == Weekly {
			if len(t.Weekdays) == 0 {
				return nil, errors.New("pick at least one day")
			}
			days := make([]string, 0, len(t.Weekdays))
			for _, d := range t.Weekdays {
				if d < 0 || d > 6 {
					return nil, fmt.Errorf("invalid weekday %d", d)
				}
				days = append(days, strconv.Itoa(d))
			}
			dow = strings.Join(days, ",")
		}
		out := make([]string, 0, len(t.Times))
		for _, hm := range t.Times {
			tm, err := time.Parse("15:04", hm)
			if err != nil {
				return nil, fmt.Errorf("%q is not a time like 05:30", hm)
			}
			out = append(out, fmt.Sprintf("%d %d * * %s", tm.Minute(), tm.Hour(), dow))
		}
		return out, nil
	case Cron:
		if len(t.Cron) == 0 {
			return nil, errors.New("add at least one cron expression")
		}
		return t.Cron, nil
	}
	return nil, nil
}

// CronLines shows what daily and weekly triggers mean as cron, for the editor.
func (t Trigger) CronLines() []string {
	s, _ := t.specs()
	return s
}

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Schedule returns nil for manual triggers.
func (t Trigger) Schedule() (cron.Schedule, error) {
	loc, err := t.Location()
	if err != nil {
		return nil, err
	}
	switch t.Kind {
	case Manual, Event:
		return nil, nil
	case Once:
		at, err := time.ParseInLocation(OnceLayout, t.OnceAt, loc)
		if err != nil {
			return nil, errors.New("pick a date and time")
		}
		return onceSchedule(at), nil
	case Daily, Weekly, Cron:
	default:
		return nil, fmt.Errorf("unknown schedule type %q", t.Kind)
	}
	specs, err := t.specs()
	if err != nil {
		return nil, err
	}
	var multi multiSchedule
	for _, spec := range specs {
		if strings.HasPrefix(strings.TrimSpace(spec), "CRON_TZ=") || strings.HasPrefix(strings.TrimSpace(spec), "TZ=") {
			return nil, errors.New("set the timezone in the timezone field, not in the expression")
		}
		s, err := parser.Parse(spec)
		if err != nil {
			return nil, fmt.Errorf("%q: %v", spec, err)
		}
		if ss, ok := s.(*cron.SpecSchedule); ok {
			ss.Location = loc
		}
		multi = append(multi, s)
	}
	return multi, nil
}

func (t Trigger) Validate() error {
	if t.Kind == Event {
		switch {
		case EventLabel(t.Event) == "":
			return errors.New("pick an event")
		case t.Event == MessageIn && strings.TrimSpace(t.Keyword) == "":
			return errors.New("enter the keyword messages must contain")
		case len(t.UserIDs) > MaxEventMembers:
			return fmt.Errorf("pick at most %d members", MaxEventMembers)
		case t.Cooldown < 0 || t.Cooldown > MaxCooldown:
			return fmt.Errorf("the cooldown must be between 0 and %d seconds", MaxCooldown)
		}
		return nil
	}
	_, err := t.Schedule()
	return err
}

// Matches reports whether an event starts a task with this trigger.
func (t Trigger) Matches(event, channelID, userID, content string) bool {
	if t.Kind != Event || t.Event != event || t.ChannelID != "" && t.ChannelID != channelID {
		return false
	}
	if len(t.UserIDs) > 0 && slices.Contains(t.UserIDs, userID) == t.Except {
		return false
	}
	return event != MessageIn || strings.Contains(strings.ToLower(content), strings.ToLower(strings.TrimSpace(t.Keyword)))
}

// Next returns up to n run times after from.
func (t Trigger) Next(from time.Time, n int) []time.Time {
	s, err := t.Schedule()
	if err != nil || s == nil {
		return nil
	}
	var out []time.Time
	for len(out) < n {
		next := s.Next(from)
		if next.IsZero() {
			break
		}
		out = append(out, next)
		from = next
	}
	return out
}

func (t Trigger) Summary() string {
	switch t.Kind {
	case Daily:
		return "Daily " + strings.Join(t.Times, ", ")
	case Weekly:
		days := make([]string, 0, len(t.Weekdays))
		for _, d := range t.Weekdays {
			if d >= 0 && d < 7 {
				days = append(days, WeekdayNames[d])
			}
		}
		return strings.Join(days, ", ") + " " + strings.Join(t.Times, ", ")
	case Once:
		if loc, err := t.Location(); err == nil {
			if at, err := time.ParseInLocation(OnceLayout, t.OnceAt, loc); err == nil {
				return "Once, " + at.Format("2 Jan 2006 15:04")
			}
		}
		return "Once"
	case Cron:
		if len(t.Cron) == 1 {
			return "Cron " + t.Cron[0]
		}
		return fmt.Sprintf("Cron %s and %d more", t.Cron[0], len(t.Cron)-1)
	case Event:
		var out string
		if t.Event == MessageIn {
			out = fmt.Sprintf("When a message contains %q", t.Keyword)
		} else {
			out = "When " + strings.ToLower(EventLabel(t.Event)[:1]) + EventLabel(t.Event)[1:]
		}
		switch n := len(t.UserIDs); {
		case n == 0:
		case t.Except && n == 1:
			out += ", all members except 1"
		case t.Except:
			out += fmt.Sprintf(", all members except %d", n)
		case n == 1:
			out += ", only 1 member"
		default:
			out += fmt.Sprintf(", only %d members", n)
		}
		return out
	}
	return "Manual only"
}

type multiSchedule []cron.Schedule

func (m multiSchedule) Next(t time.Time) time.Time {
	var best time.Time
	for _, s := range m {
		n := s.Next(t)
		if !n.IsZero() && (best.IsZero() || n.Before(best)) {
			best = n
		}
	}
	return best
}

// onceSchedule fires once; robfig/cron never runs an entry whose next time is zero.
type onceSchedule time.Time

func (o onceSchedule) Next(t time.Time) time.Time {
	at := time.Time(o)
	if t.Before(at) {
		return at
	}
	return time.Time{}
}

// Overlap is another event task that starts on the same events as a task.
type Overlap struct {
	Task    Task
	UserIDs []string // the members both start for, nil for (nearly) every member
}

// EventOverlaps lists the enabled event tasks of others that start on the
// same event, channel and keyword as t for some members.
func EventOverlaps(t Task, others []Task) []Overlap {
	tr := t.Trigger
	if tr.Kind != Event {
		return nil
	}
	var out []Overlap
	for _, o := range others {
		ot := o.Trigger
		if o.ID == t.ID || !o.Enabled || o.GuildID != t.GuildID || ot.Kind != Event || ot.Event != tr.Event ||
			tr.ChannelID != "" && ot.ChannelID != "" && tr.ChannelID != ot.ChannelID || tr.Event == MessageIn && !keywordsOverlap(tr.Keyword, ot.Keyword) {
			continue
		}
		ids, ok := sharedMembers(tr, ot)
		if ok {
			out = append(out, Overlap{Task: o, UserIDs: ids})
		}
	}
	return out
}

func keywordsOverlap(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	return strings.Contains(a, b) || strings.Contains(b, a)
}

// sharedMembers are the members both triggers start for: a list when one
// names its members, nil when both start for every member or all but some.
func sharedMembers(a, b Trigger) ([]string, bool) {
	aOnly, bOnly := len(a.UserIDs) > 0 && !a.Except, len(b.UserIDs) > 0 && !b.Except
	switch {
	case !aOnly && !bOnly:
		return nil, true
	case aOnly && bOnly:
		var ids []string
		for _, id := range a.UserIDs {
			if slices.Contains(b.UserIDs, id) {
				ids = append(ids, id)
			}
		}
		return ids, len(ids) > 0
	case aOnly:
		a, b = b, a
	}
	// b names its members, a starts for all or all but some
	var ids []string
	for _, id := range b.UserIDs {
		if !a.Except || !slices.Contains(a.UserIDs, id) {
			ids = append(ids, id)
		}
	}
	return ids, len(ids) > 0
}
