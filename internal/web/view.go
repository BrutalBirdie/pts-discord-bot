package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// displayLoc is the timezone of the settings, used for every time on the pages.
var displayLoc atomic.Pointer[time.Location]

func SetDisplayTimezone(name string) {
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = time.UTC
	}
	displayLoc.Store(loc)
}

func loc() *time.Location {
	if l := displayLoc.Load(); l != nil {
		return l
	}
	return time.UTC
}

var viewFuncs = template.FuncMap{
	"initial": initial,
	"icon": func(name string) template.HTML {
		return template.HTML(`<svg class="i" aria-hidden="true"><use href="#i-` + template.HTMLEscapeString(name) + `"/></svg>`)
	},
	"dict": func(kv ...any) (map[string]any, error) {
		if len(kv)%2 != 0 {
			return nil, errors.New("dict needs key value pairs")
		}
		m := make(map[string]any, len(kv)/2)
		for i := 0; i < len(kv); i += 2 {
			k, ok := kv[i].(string)
			if !ok {
				return nil, errors.New("dict keys must be strings")
			}
			m[k] = kv[i+1]
		}
		return m, nil
	},
	"initials": initials,
	"when":     when,
	"until":    until,
	"ago":      ago,
	"since":    time.Since,
	"nowTime":  time.Now,
	"dur":      dur,
	"clock":    clock,
	"ms":       func(d time.Duration) int { return int(d.Milliseconds()) },
	"bytes":    humanBytes,
	"add":      func(a, b int) int { return a + b },
	"join":     strings.Join,
	"tz":       func() string { return loc().String() },
	"mentions": mentions,
	"expand": func(s string) string {
		return messages.Expand(s, messages.Vars{Now: time.Now().In(loc()), Channel: "#channel", Server: "Server"})
	},
	"hasInt": func(list []int, v int) bool { return slices.Contains(list, v) },
	"hasChannel": func(list []bot.Channel, id string) bool {
		return slices.ContainsFunc(list, func(c bot.Channel) bool { return c.ID == id })
	},
	"hasString":  func(list []string, v string) bool { return slices.Contains(list, v) },
	"eventLabel": tasks.EventLabel,
	"untilLabel": tasks.UntilLabel,
	"untils":     func() []string { return tasks.Untils },
	"hasID":      func(list []int64, id int64) bool { return slices.Contains(list, id) },
	"hasTask": func(list []taskChoice, id int64) bool {
		return slices.ContainsFunc(list, func(t taskChoice) bool { return t.ID == id })
	},
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.Itoa(n) + " " + many
	},
}

func initial(s string) string {
	for _, r := range strings.TrimSpace(s) {
		return strings.ToUpper(string(r))
	}
	return "?"
}

func initials(name string) string {
	var out []rune
	for _, f := range strings.Fields(name) {
		out = append(out, []rune(strings.ToUpper(f))[0])
		if len(out) == 2 {
			break
		}
	}
	return string(out)
}

func dayStart(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// when shows a time relative to today in the display timezone: "Today 16:35", "Fri 05:00", "5 Nov 09:12".
func when(t time.Time) string {
	t = t.In(loc())
	now := time.Now().In(loc())
	days := int(dayStart(t).Sub(dayStart(now)).Hours() / 24)
	clock := t.Format("15:04")
	switch {
	case days == 0:
		return "Today " + clock
	case days == 1:
		return "Tomorrow " + clock
	case days == -1:
		return "Yesterday " + clock
	case days > 1 && days < 7, days < -1 && days > -7:
		return t.Format("Mon ") + clock
	case t.Year() == now.Year():
		return t.Format("2 Jan ") + clock
	}
	return t.Format("2 Jan 2006 ") + clock
}

func until(t time.Time) string {
	d := time.Until(t)
	if d < time.Minute {
		return "now"
	}
	return "in " + dur(d)
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " h ago"
	case d < 48*time.Hour:
		return "yesterday"
	}
	return strconv.Itoa(int(d.Hours()/24)) + " days ago"
}

func dur(d time.Duration) string {
	switch {
	case d < time.Second:
		return "0 s"
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + " s"
	case d < time.Hour:
		return fmt.Sprintf("%d min %d s", int(d.Minutes()), int(d.Seconds())%60)
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	}
	return strconv.Itoa(int(d.Hours()/24)) + " days"
}

// clock formats milliseconds as 3:45.
func clock(ms int) string {
	s := ms / 1000
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) + " " + string("KMGTPE"[exp]) + "B"
}

// mentions escapes text and shows <@id> mentions as highlighted names, for the preview.
func mentions(text string, names map[string]string) template.HTML {
	var b strings.Builder
	last := 0
	for _, m := range messages.MentionRe.FindAllStringSubmatchIndex(text, -1) {
		b.WriteString(template.HTMLEscapeString(text[last:m[0]]))
		id := text[m[2]:m[3]]
		name := names[id]
		if name == "" {
			name = id
		}
		b.WriteString(`<span class="dmention">@` + template.HTMLEscapeString(name) + `</span>`)
		last = m[1]
	}
	b.WriteString(template.HTMLEscapeString(text[last:]))
	return template.HTML(b.String())
}
