package bot

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/disgoorg/disgo/rest"
)

// Label is how the member picker shows and accepts a member: "Name (@username)".
func (m Member) Label() string { return m.Name + " (@" + m.Username + ")" }

// MatchMember finds the member a picker value means: an ID, a <@mention>, a
// label, @username, username or an unambiguous display name.
func MatchMember(members []Member, text string) (Member, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Member{}, false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(text, "<@"), "!"), ">")
	if _, ok := ParseID(id); ok {
		for _, m := range members {
			if m.ID == id {
				return m, true
			}
		}
		return Member{ID: id, Name: id}, true
	}
	if i := strings.LastIndex(text, "(@"); i >= 0 && strings.HasSuffix(text, ")") {
		text = text[i+1 : len(text)-1]
	}
	user := strings.ToLower(strings.TrimPrefix(text, "@"))
	var byName []Member
	for _, m := range members {
		if strings.ToLower(m.Username) == user {
			return m, true
		}
		if strings.EqualFold(m.Name, text) {
			byName = append(byName, m)
		}
	}
	if len(byName) == 1 {
		return byName[0], true
	}
	return Member{}, false
}

// SearchMembers returns up to limit members whose name or username contains q,
// members whose name starts with q first. Bots are left out.
func SearchMembers(members []Member, q string, limit int) []Member {
	q = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q), "@"))
	type hit struct {
		m    Member
		rank int
	}
	var hits []hit
	for _, m := range members {
		if m.Bot {
			continue
		}
		name, user := strings.ToLower(m.Name), strings.ToLower(m.Username)
		switch {
		case q == "" || strings.HasPrefix(name, q) || strings.HasPrefix(user, q):
			hits = append(hits, hit{m, 0})
		case strings.Contains(name, q) || strings.Contains(user, q) || m.ID == q:
			hits = append(hits, hit{m, 1})
		}
	}
	slices.SortStableFunc(hits, func(a, b hit) int { return cmp.Compare(a.rank, b.rank) })
	out := make([]Member, 0, min(limit, len(hits)))
	for _, h := range hits[:min(limit, len(hits))] {
		out = append(out, h.m)
	}
	return out
}

// ResolveMember maps a picker value to a member of the guild.
func (b *Bot) ResolveMember(guildID, text string) (Member, bool) {
	return MatchMember(b.Members(guildID), text)
}

// MemberLabel is the picker text for a stored user ID.
func (b *Bot) MemberLabel(guildID, userID string) string {
	if userID == "" {
		return ""
	}
	if m, ok := MatchMember(b.Members(guildID), userID); ok && m.Username != "" {
		return m.Label()
	}
	return userID
}

// IsMember asks the member cache and then Discord whether a user is in a
// server. It is false as well when the bot is not in that server any more.
func (b *Bot) IsMember(ctx context.Context, guildID, userID string) (bool, error) {
	c, err := b.get()
	if err != nil {
		return false, err
	}
	gid, ok1 := ParseID(guildID)
	uid, ok2 := ParseID(userID)
	if !ok1 || !ok2 {
		return false, nil
	}
	if _, ok := c.Caches.Member(gid, uid); ok {
		return true, nil
	}
	_, err = c.Rest.GetMember(gid, uid, rest.WithCtx(ctx))
	var re *rest.Error
	if errors.As(err, &re) {
		switch re.Code {
		case rest.JSONErrorCodeUnknownMember, rest.JSONErrorCodeUnknownUser, rest.JSONErrorCodeUnknownGuild, rest.JSONErrorCodeMissingAccess:
			return false, nil
		}
	}
	return err == nil, err
}
