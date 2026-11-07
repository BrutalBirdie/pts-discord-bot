package tasks

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	MinWait = 100 * time.Millisecond
	MaxWait = 24 * time.Hour
	// DefaultUntilWait is the timeout a wait gets when a condition is picked.
	DefaultUntilWait = 5 * time.Minute
)

// Conditions a wait step can wait for. The member ones watch UserID, the
// channel ones ChannelID.
const (
	UntilUndeafened = "undeafened"
	UntilUnmuted    = "unmuted"
	UntilInVoice    = "in_voice"
	UntilLeftVoice  = "left_voice"
	UntilOccupied   = "channel_occupied"
	UntilEmpty      = "channel_empty"
)

var Untils = []string{UntilUndeafened, UntilUnmuted, UntilInVoice, UntilLeftVoice, UntilOccupied, UntilEmpty}

const (
	OnTimeoutContinue = "continue"
	OnTimeoutStop     = "stop"
)

func UntilLabel(u string) string {
	return map[string]string{UntilUndeafened: "Until a member is undeafened", UntilUnmuted: "Until a member is unmuted",
		UntilInVoice: "Until a member is in voice", UntilLeftVoice: "Until a member has left voice",
		UntilOccupied: "Until the channel has someone in it", UntilEmpty: "Until the channel is empty"}[u]
}

// MemberUntil reports whether the condition watches a member.
func MemberUntil(u string) bool {
	return u == UntilUndeafened || u == UntilUnmuted || u == UntilInVoice || u == UntilLeftVoice
}

// ChannelUntil reports whether the condition needs a voice channel.
func ChannelUntil(u string) bool { return u == UntilOccupied || u == UntilEmpty }

// WaitDuration is how long a wait step waits, or its timeout with a condition.
// Steps saved before wait_ms existed have whole seconds.
func (s Step) WaitDuration() time.Duration {
	if s.WaitMS > 0 {
		return time.Duration(s.WaitMS) * time.Millisecond
	}
	return time.Duration(s.Seconds) * time.Second
}

// WaitValue is the text of the duration field in the editor.
func (s Step) WaitValue() string {
	if s.WaitInput != "" {
		return s.WaitInput
	}
	if d := s.WaitDuration(); d > 0 {
		return FormatDuration(d)
	}
	return ""
}

// UntilText describes the condition, member and channel are display names.
func (s Step) UntilText(member, channel string) string {
	if s.ToTrigger {
		member = "the member who started the run"
	}
	in := "voice"
	if s.ChannelID != "" {
		in = channel
	}
	switch s.Until {
	case UntilUndeafened:
		return "until " + member + " is undeafened"
	case UntilUnmuted:
		return "until " + member + " is unmuted"
	case UntilInVoice:
		return "until " + member + " is in " + in
	case UntilLeftVoice:
		return "until " + member + " has left " + in
	case UntilOccupied:
		return "until someone is in " + channel
	case UntilEmpty:
		return "until " + channel + " is empty"
	}
	return ""
}

func (s Step) validateWait() error {
	if s.WaitInput != "" {
		if _, err := ParseDuration(s.WaitInput); err != nil {
			return err
		}
	}
	d := s.WaitDuration()
	if d == 0 {
		return errors.New("enter how long to wait, e.g. 1.5s or 5m")
	}
	if err := checkWait(d); err != nil {
		return err
	}
	switch {
	case s.Until == "":
	case !slices.Contains(Untils, s.Until):
		return fmt.Errorf("unknown wait condition %q", s.Until)
	case MemberUntil(s.Until) && s.UserID == "" && !s.ToTrigger:
		return errors.New("pick the member to wait for")
	case ChannelUntil(s.Until) && s.ChannelID == "":
		return errors.New("pick the voice channel to watch")
	}
	if s.OnTimeout != "" && s.OnTimeout != OnTimeoutContinue && s.OnTimeout != OnTimeoutStop {
		return fmt.Errorf("unknown timeout action %q", s.OnTimeout)
	}
	return nil
}

func checkWait(d time.Duration) error {
	if d < MinWait {
		return errors.New("wait at least 100ms")
	}
	if d > MaxWait {
		return errors.New("wait at most 24h")
	}
	return nil
}

var durationUnits = map[string]time.Duration{"ms": time.Millisecond, "s": time.Second, "sec": time.Second, "m": time.Minute, "min": time.Minute,
	"h": time.Hour, "d": 24 * time.Hour}

// ParseDuration reads wait durations like 500ms, 1.5s, 1m30s or 2h. A bare
// number is seconds and a comma works as decimal point.
func ParseDuration(in string) (time.Duration, error) {
	if strings.TrimSpace(in) == "" {
		return 0, errors.New("enter how long to wait, e.g. 1.5s or 5m")
	}
	d, err := parseSpan(in)
	if errors.Is(err, errTooLong) {
		return 0, errors.New("wait at most 24h")
	}
	if err != nil {
		return 0, err
	}
	if err := checkWait(d); err != nil {
		return 0, err
	}
	return d, nil
}

// ParseCooldown reads a cooldown in the format of ParseDuration, rounded to
// whole seconds, from 0 to MaxCooldown.
func ParseCooldown(in string) (int, error) {
	d, err := parseSpan(in)
	if errors.Is(err, errTooLong) || err == nil && d > MaxCooldown*time.Second {
		return 0, errors.New("the cooldown is at most 24h")
	}
	if err != nil {
		return 0, err
	}
	return int(d.Round(time.Second) / time.Second), nil
}

var errTooLong = errors.New("too long")

// parseSpan reads the format of ParseDuration without limits but errTooLong for days and more past 24h.
func parseSpan(in string) (time.Duration, error) {
	s := strings.ReplaceAll(strings.ToLower(strings.Join(strings.Fields(in), "")), ",", ".")
	bad := fmt.Errorf("%q is not a duration, use e.g. 500ms, 1.5s, 5m or 2h", strings.TrimSpace(in))
	if s == "" {
		return 0, bad
	}
	var total float64
	for rest := s; rest != ""; {
		i := strings.IndexFunc(rest, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
		if i == -1 {
			i = len(rest)
		}
		num, after := rest[:i], rest[i:]
		j := strings.IndexFunc(after, func(r rune) bool { return r < 'a' || r > 'z' })
		if j == -1 {
			j = len(after)
		}
		unit := after[:j]
		rest = after[j:]
		n, err := strconv.ParseFloat(num, 64)
		if err != nil || strings.Trim(num, ".") == "" {
			return 0, bad
		}
		if unit == "" && num == s {
			unit = "s"
		}
		if unit == "y" || unit == "w" {
			return 0, errTooLong
		}
		u, ok := durationUnits[unit]
		if !ok {
			return 0, bad
		}
		total += n * float64(u)
	}
	if total > float64(MaxWait) {
		return 0, errTooLong
	}
	return time.Duration(math.Round(total/float64(time.Millisecond))) * time.Millisecond, nil
}

// FormatDuration is the shortest form ParseDuration reads back, like 1.5s or 1h30m.
func FormatDuration(d time.Duration) string {
	d = d.Round(time.Millisecond)
	if d < time.Second {
		return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
	}
	var b strings.Builder
	if h := d / time.Hour; h > 0 {
		b.WriteString(strconv.FormatInt(int64(h), 10) + "h")
		d -= h * time.Hour
	}
	if m := d / time.Minute; m > 0 {
		b.WriteString(strconv.FormatInt(int64(m), 10) + "m")
		d -= m * time.Minute
	}
	if d > 0 {
		b.WriteString(strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s")
	}
	return b.String()
}
