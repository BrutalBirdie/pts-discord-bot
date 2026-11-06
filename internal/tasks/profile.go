package tasks

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxNick is the nickname limit of https://docs.discord.com/developers/resources/guild#modify-guild-member
const MaxNick = 32

// MaxAvatarBytes is the largest image a profile step sends as avatar.
const MaxAvatarBytes = 8 << 20

// ProfileMaxWait is how long a profile step waits for a rate limit before it skips the run.
const ProfileMaxWait = 30 * time.Second

// Profile fields, each tracked on its own against rate limits.
const (
	FieldNick   = "nick"
	FieldAvatar = "avatar"
)

// profileGap spaces changes of one field in one server. Discord publishes no
// numbers for this route, so the gaps are our own and kept low for nicknames.
// https://docs.discord.com/developers/topics/rate-limits
var profileGap = map[string]time.Duration{FieldNick: 5 * time.Second, FieldAvatar: time.Minute}

func ProfileGap(field string) time.Duration { return profileGap[field] }

// ProfileLimit is what is known about one field in one server.
type ProfileLimit struct {
	ChangedAt    time.Time
	BlockedUntil time.Time // from the retry_after of a 429
}

// ProfileFields lists the fields a profile step changes.
func (s Step) ProfileFields() []string {
	var out []string
	if s.Nick != "" || s.ResetNick {
		out = append(out, FieldNick)
	}
	if s.AvatarMediaID != 0 || s.ResetAvatar {
		out = append(out, FieldAvatar)
	}
	return out
}

// ProfileDecide says when a change of fields may go out: right away, after
// wait, or not in this run when that is later than ProfileMaxWait.
func ProfileDecide(now time.Time, limits map[string]ProfileLimit, fields []string) (wait time.Duration, at time.Time, skip bool) {
	at = now
	for _, f := range fields {
		l := limits[f]
		if !l.ChangedAt.IsZero() {
			if next := l.ChangedAt.Add(ProfileGap(f)); next.After(at) {
				at = next
			}
		}
		if l.BlockedUntil.After(at) {
			at = l.BlockedUntil
		}
	}
	wait = at.Sub(now)
	return wait, at, wait > ProfileMaxWait
}

func (s Step) validateProfile() error {
	if s.Nick == "" && !s.ResetNick && s.AvatarMediaID == 0 && !s.ResetAvatar {
		return errors.New("pick a nickname or an avatar to change")
	}
	if s.Nick != "" && s.ResetNick || s.AvatarMediaID != 0 && s.ResetAvatar {
		return errors.New("set or reset, not both")
	}
	if utf8.RuneCountInString(s.Nick) > MaxNick {
		return fmt.Errorf("the nickname is longer than %d characters", MaxNick)
	}
	return nil
}

// ProfileText describes the step, image is the name of the avatar image.
func (s Step) ProfileText(image string) string {
	var parts []string
	switch {
	case s.ResetNick:
		parts = append(parts, "reset nickname")
	case s.Nick != "":
		parts = append(parts, "set nickname to "+s.Nick)
	}
	switch {
	case s.ResetAvatar:
		parts = append(parts, "reset avatar")
	case s.AvatarMediaID != 0:
		parts = append(parts, "set avatar to "+image)
	}
	return strings.Join(parts, " and ")
}
