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
)

var TriggerKinds = []TriggerKind{Daily, Weekly, Once, Cron, Manual}

// OnceLayout is the format of an <input type="datetime-local">.
const OnceLayout = "2006-01-02T15:04"

type Trigger struct {
	Kind     TriggerKind `json:"kind"`
	Times    []string    `json:"times,omitempty"`    // HH:MM, daily and weekly
	Weekdays []int       `json:"weekdays,omitempty"` // 0 is Sunday, as in cron
	OnceAt   string      `json:"once_at,omitempty"`  // OnceLayout in Timezone
	Cron     []string    `json:"cron,omitempty"`     // standard 5 field expressions
	Timezone string      `json:"timezone"`
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
	case Manual:
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
	_, err := t.Schedule()
	return err
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
