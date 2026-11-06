package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
)

const accessPerPage = 25

var accessStates = []string{store.AccessPending, store.AccessApproved, store.AccessDenied, store.AccessBlocked}

type accessServer struct {
	ID, Name string
}

// accessRow is one Discord user on the admin Access tab.
type accessRow struct {
	ID, Name, Username, Avatar string
	State                      string
	Sub                        string
	Chip, ChipClass            string
	Note                       string
	ServersLabel               string
	Servers                    []accessServer
	Editable                   bool // pending: the admin picks which servers to approve
}

type accessTab struct {
	State  string
	Counts map[string]int
	Rows   []accessRow
	Search string
	Page   int
	Pages  int
	Total  int
}

func day(t time.Time) string {
	t = t.In(loc())
	if t.Year() == time.Now().In(loc()).Year() {
		return t.Format("2 Jan")
	}
	return t.Format("2 Jan 2006")
}

func by(actor string) string {
	if actor == "" {
		return ""
	}
	return " by " + actor
}

// newAccessRow turns an entry into what the Access tab shows, guild names from name.
func newAccessRow(e store.AccessEntry, name func(string) string, now time.Time) accessRow {
	u := e.User
	row := accessRow{ID: u.ID, Name: u.Name(), Username: u.Username, Avatar: avatarURL(u), State: e.State, Chip: e.State}
	servers := func(ids []string) {
		for _, id := range ids {
			row.Servers = append(row.Servers, accessServer{ID: id, Name: name(id)})
		}
	}
	req := e.Request
	if req != nil {
		row.Note = req.Note
	}
	switch e.State {
	case store.AccessPending:
		row.Sub = "Requested " + ago(req.RequestedAt)
		days := int(dayStart(req.ExpiresAt.In(loc())).Sub(dayStart(now.In(loc()))).Hours() / 24)
		switch {
		case days <= 0:
			row.Chip, row.ChipClass = "denied today", "warn"
		case days == 1:
			row.Chip, row.ChipClass = "denied tomorrow", "warn"
		default:
			row.Chip = "denied in " + strconv.Itoa(days) + " days"
		}
		row.ServersLabel, row.Editable = "Asks for", true
		servers(req.GuildIDs)
	case store.AccessApproved:
		row.ChipClass = "ok"
		if e.GrantedAt != nil {
			row.Sub = "Approved" + by(e.GrantedBy) + " on " + day(*e.GrantedAt)
		}
		row.ServersLabel = "Can manage"
		servers(e.Grants)
		row.Note = ""
	case store.AccessDenied:
		if req != nil {
			at := ""
			if req.DecidedAt != nil {
				at = " on " + day(*req.DecidedAt)
			}
			switch {
			case req.Status == store.AccessRevoked:
				row.Sub = "Access revoked" + by(req.DecidedBy) + at + ". Can ask again."
			case req.DecidedBy == store.SystemActor:
				row.Sub = "Denied automatically" + at + ", nobody answered in 7 days. Can ask again."
			default:
				row.Sub = "Denied" + by(req.DecidedBy) + at + ". Can ask again."
			}
			row.ServersLabel = "Asked for"
			servers(req.GuildIDs)
		}
	case store.AccessBlocked:
		row.ChipClass = "danger"
		row.Sub = "Blocked" + by(u.BlockedBy)
		if u.BlockedAt != nil {
			row.Sub += " on " + day(*u.BlockedAt)
		}
		if req != nil {
			row.ServersLabel = "Asked for"
			servers(req.GuildIDs)
		}
	}
	return row
}

func (s *Server) accessTab(r *http.Request) (*accessTab, error) {
	ctx := r.Context()
	q := r.URL.Query()
	t := &accessTab{State: q.Get("state"), Search: strings.TrimSpace(q.Get("q"))}
	if !slices.Contains(accessStates, t.State) {
		t.State = store.AccessPending
	}
	t.Page, _ = strconv.Atoi(q.Get("page"))
	t.Page = max(t.Page, 1)
	var err error
	if t.Counts, err = s.store.AccessCounts(ctx); err != nil {
		return nil, err
	}
	var guildIDs []string
	if t.Search != "" {
		for _, g := range s.bot.Guilds() {
			if strings.Contains(strings.ToLower(g.Name), strings.ToLower(t.Search)) {
				guildIDs = append(guildIDs, g.ID)
			}
		}
	}
	list, total, err := s.store.ListAccess(ctx, store.AccessQuery{State: t.State, Search: t.Search, GuildIDs: guildIDs, Limit: accessPerPage, Offset: (t.Page - 1) * accessPerPage})
	if err != nil {
		return nil, err
	}
	t.Total, t.Pages = total, max(1, (total+accessPerPage-1)/accessPerPage)
	now := time.Now()
	for _, e := range list {
		t.Rows = append(t.Rows, newAccessRow(e, s.bot.GuildName, now))
	}
	return t, nil
}

// accessURL is the Access tab with the filter of a form post, to go back to after an action.
func accessURL(state, search, page string) string {
	v := url.Values{}
	if slices.Contains(accessStates, state) {
		v.Set("state", state)
	}
	if search = strings.TrimSpace(search); search != "" && len(search) <= 200 {
		v.Set("q", search)
	}
	if n, err := strconv.Atoi(page); err == nil && n > 1 {
		v.Set("page", strconv.Itoa(n))
	}
	if len(v) == 0 {
		return "/admin/access"
	}
	return "/admin/access?" + v.Encode()
}

func (s *Server) decideAccess(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := r.PathValue("user")
	if _, ok := bot.ParseID(uid); !ok {
		s.notFound(w, r)
		return
	}
	acc, err := s.store.DiscordAccess(ctx, uid)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	f := r.PostForm
	admin := sessionFrom(r).Username
	target := "@" + acc.User.Username
	detail, done := "", ""
	do := f.Get("do")
	switch do {
	case "approve":
		var guilds []string
		for _, id := range f["guild"] {
			if !slices.Contains(guilds, id) {
				guilds = append(guilds, id)
			}
		}
		if len(guilds) == 0 {
			s.fail(w, r, http.StatusBadRequest, "Pick at least one server to approve.")
			return
		}
		err = s.store.ApproveAccess(ctx, uid, guilds, admin)
		names := make([]string, 0, len(guilds))
		for _, id := range guilds {
			names = append(names, s.bot.GuildName(id))
		}
		detail, done = strings.Join(names, ", "), "access-approved"
	case "deny":
		err, done = s.store.DenyAccess(ctx, uid, admin), "access-denied"
	case "block":
		err, done = s.store.BlockDiscordUser(ctx, uid, admin), "access-blocked"
	case "unblock":
		err, done = s.store.UnblockDiscordUser(ctx, uid), "access-unblocked"
	case "revoke":
		err, done = s.store.RevokeAccess(ctx, uid, admin), "access-revoked"
	default:
		s.fail(w, r, http.StatusBadRequest, "Unknown action.")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusConflict, "This changed in the meantime. Reload the Access tab and try again.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r, "access."+do, target, detail)
	redirect(w, r, accessURL(f.Get("state"), f.Get("q"), f.Get("page")), done)
}

type accessKeeper interface {
	AutoDenyExpired(ctx context.Context) ([]store.AccessRequest, error)
	AllGrants(ctx context.Context) ([]store.Grant, error)
	RemoveGrant(ctx context.Context, userID, guildID string) error
}

type memberChecker interface {
	IsMember(ctx context.Context, guildID, userID string) (bool, error)
}

// maintainAccess denies requests nobody answered and ends access to servers
// the user or the bot left. When Discord cannot answer, access stays until the next run.
func maintainAccess(ctx context.Context, st accessKeeper, mc memberChecker, audit func(action, target, detail string)) error {
	denied, err := st.AutoDenyExpired(ctx)
	if err != nil {
		return err
	}
	for _, d := range denied {
		audit("access.autodeny", store.DiscordSubject(d.UserID), "nobody answered in 7 days")
	}
	grants, err := st.AllGrants(ctx)
	if err != nil {
		return err
	}
	for _, g := range grants {
		ok, err := mc.IsMember(ctx, g.GuildID, g.UserID)
		if err != nil || ok {
			continue
		}
		if err := st.RemoveGrant(ctx, g.UserID, g.GuildID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return err
		}
		audit("access.leave", store.DiscordSubject(g.UserID), "not in server "+g.GuildID+" any more")
	}
	return nil
}

func (s *Server) guildLeft(guildID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	users, err := s.store.RemoveGuildGrants(ctx, guildID)
	if err != nil {
		s.log.Error("removing access to a server the bot left", "guild", guildID, "err", err)
		return
	}
	for _, u := range users {
		s.auditAs(ctx, store.SystemActor, "access.leave", store.DiscordSubject(u), "the bot left server "+guildID)
	}
}
