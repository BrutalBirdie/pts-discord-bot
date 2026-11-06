package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"
)

// ProfileChange is a change of the bot's own member in one server. Nil
// fields stay as they are.
type ProfileChange struct {
	Nick        *string // "" resets to the bot's name
	Avatar      *discord.Icon
	ResetAvatar bool
}

// RateLimitedError means Discord refuses the change until Until.
type RateLimitedError struct{ Until time.Time }

func (e *RateLimitedError) Error() string {
	return "Discord rate limited the change until " + e.Until.UTC().Format(time.TimeOnly) + " UTC"
}

// UpdateProfile changes nickname and avatar of the bot in a server. disgo
// waits for rate limits on its own, ctx bounds that wait.
// https://docs.discord.com/developers/resources/guild#modify-current-member
func (b *Bot) UpdateProfile(ctx context.Context, guildID snowflake.ID, p ProfileChange) error {
	c, err := b.get()
	if err != nil {
		return err
	}
	if p.Nick != nil {
		if self, ok := c.Caches.SelfMember(guildID); ok && !c.Caches.MemberPermissions(self).Has(discord.PermissionChangeNickname) {
			return &PermanentError{"The bot needs the Change Nickname permission in this server."}
		}
	}
	upd := discord.CurrentMemberUpdate{Nick: p.Nick}
	if p.Avatar != nil {
		upd.Avatar = omit.New(p.Avatar)
	} else if p.ResetAvatar {
		upd.Avatar = omit.NewNilPtr[discord.Icon]()
	}
	start := time.Now()
	_, err = c.Rest.UpdateCurrentMember(guildID, upd, rest.WithCtx(ctx))
	if err == nil {
		return nil
	}
	if until, ok := b.limits.since(guildID, start); ok {
		return &RateLimitedError{Until: until}
	}
	var re *rest.Error
	if errors.As(err, &re) && re.Response != nil && re.Response.StatusCode == http.StatusTooManyRequests ||
		errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return &RateLimitedError{Until: time.Now().Add(time.Minute)}
	}
	return err
}

// profileLimits keeps the retry_after of 429 answers to profile changes,
// disgo retries them itself and does not return it.
type profileLimits struct {
	mu sync.Mutex
	m  map[snowflake.ID]limitHit
}

type limitHit struct{ at, until time.Time }

func (l *profileLimits) record(guildID snowflake.ID, until time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[snowflake.ID]limitHit{}
	}
	l.m[guildID] = limitHit{at: time.Now(), until: until}
}

// since returns the limit Discord reported after start, if it is still running.
func (l *profileLimits) since(guildID snowflake.ID, start time.Time) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.m[guildID]
	if !ok || h.at.Before(start) || !h.until.After(time.Now()) {
		return time.Time{}, false
	}
	return h.until, true
}

type limitTransport struct {
	base   http.RoundTripper
	limits *profileLimits
}

func (t *limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rs, err := t.base.RoundTrip(req)
	if err != nil || rs.StatusCode != http.StatusTooManyRequests || req.Method != http.MethodPatch {
		return rs, err
	}
	guild, ok := profileGuild(req.URL.Path)
	if !ok {
		return rs, nil
	}
	body, err := io.ReadAll(rs.Body)
	_ = rs.Body.Close()
	rs.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return rs, nil
	}
	t.limits.record(guild, time.Now().Add(RetryAfter(rs.Header, body)))
	return rs, nil
}

// profileGuild finds the guild of PATCH /guilds/{guild.id}/members/@me.
func profileGuild(path string) (snowflake.ID, bool) {
	head, ok := strings.CutSuffix(path, "/members/@me")
	if !ok {
		return 0, false
	}
	i := strings.LastIndex(head, "/guilds/")
	if i < 0 {
		return 0, false
	}
	return ParseID(head[i+len("/guilds/"):])
}

// RetryAfter reads how long a 429 asks to wait, the retry_after of the body
// first as it has fractions. https://docs.discord.com/developers/topics/rate-limits
func RetryAfter(h http.Header, body []byte) time.Duration {
	var v struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &v) == nil && v.RetryAfter > 0 {
		return time.Duration(math.Round(v.RetryAfter*1000)) * time.Millisecond
	}
	if n, err := strconv.ParseFloat(h.Get("Retry-After"), 64); err == nil && n > 0 {
		return time.Duration(math.Round(n*1000)) * time.Millisecond
	}
	return time.Minute
}
