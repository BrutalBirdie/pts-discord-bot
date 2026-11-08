package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// place is where a page sits in the Control Room: in a server, or under Home
// when Guild is empty.
type place struct {
	Guild   string
	Section string // home: overview, trash, servers, media, messages; server: tasks, messages, media, settings, "" for channels and members
	Members bool   // the member column is shown
}

type placeKey struct{}

// at puts the page of this request in a place of the Control Room.
func at(r *http.Request, p place) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), placeKey{}, p))
}

// homeSections are the pages that sit under Home by default.
var homeSections = map[string]string{"dashboard": "overview", "trash": "trash", "servers": "servers", "media": "media", "messages": "messages"}

// shell is the frame of every page: the server rail, the channel list of the
// open server or the Home list, and the member column.
type shell struct {
	Guilds  []bot.Guild
	Guild   bot.Guild
	Section string
	Path    string // this page with its query, for forms that come back here
	query   url.Values

	Text      []roomChannel
	Voice     []roomChannel
	Locked    []roomChannel
	TaskCount int
	Channel   string // the open channel
	Member    string // the open member

	Members  bool
	MGroups  []memberGroup
	MQuery   string
	MPage    int
	MMore    bool
	MHidden  int
	Presence bool
}

// Home tells whether the page sits under Home.
func (sh *shell) Home() bool { return sh.Guild.ID == "" }

// URL is a view of the open server: a channel with c, a member with m.
func (sh *shell) URL(key, value string) string {
	if key == "" {
		return "/room/" + sh.Guild.ID
	}
	return "/room/" + sh.Guild.ID + "?" + url.Values{key: {value}}.Encode()
}

// In is a page of the open server, like tasks or media.
func (sh *shell) In(section string) string { return "/room/" + sh.Guild.ID + "/" + section }

// MPrev and MNext page through the member column, MKeep are the query values a member search keeps.
func (sh *shell) MPrev() string { return sh.mlink(sh.MPage - 1) }
func (sh *shell) MNext() string { return sh.mlink(sh.MPage + 1) }

func (sh *shell) mlink(page int) string {
	v := url.Values{}
	for k, vs := range sh.query {
		v[k] = vs
	}
	v.Set("mp", strconv.Itoa(page))
	return sh.pathOnly() + "?" + v.Encode()
}

func (sh *shell) pathOnly() string { return strings.SplitN(sh.Path, "?", 2)[0] }

// MAction is where the member search goes, MKeep the values it keeps.
func (sh *shell) MAction() string { return sh.pathOnly() }

func (sh *shell) MKeep() map[string]string {
	out := map[string]string{}
	for k, vs := range sh.query {
		if k != "mq" && k != "mp" && k != "q" && k != "page" && len(vs) > 0 {
			out[k] = vs[0]
		}
	}
	return out
}

// buildShell gathers the frame of a page for a user with access.
func (s *Server) buildShell(r *http.Request, page string) *shell {
	p, _ := r.Context().Value(placeKey{}).(place)
	q := r.URL.Query()
	q.Del("done")
	q.Del("undo")
	q.Del("as")
	sh := &shell{Guilds: s.guilds(r), Section: p.Section, Path: r.URL.Path, query: q}
	if len(q) > 0 {
		sh.Path += "?" + q.Encode()
	}
	if p.Guild == "" {
		if sh.Section == "" {
			sh.Section = homeSections[page]
		}
		return sh
	}
	g, ok := s.bot.Guild(p.Guild)
	if !ok || !sessionFrom(r).CanGuild(g.ID) {
		return sh
	}
	sh.Guild = g
	if sh.Section == "" && strings.HasPrefix(r.URL.Path, "/room/") {
		sh.Channel, sh.Member = q.Get("c"), q.Get("m")
	}
	var all []tasks.Task
	if s.store != nil {
		list, err := s.store.ListTasks(r.Context(), store.GuildScope(g.ID))
		if err != nil {
			s.log.Error("listing the tasks of a server", "err", err)
		}
		all = slices.DeleteFunc(list, func(t tasks.Task) bool { return t.GuildID != g.ID })
	}
	sh.TaskCount = len(all)
	for _, c := range s.bot.Channels(g.ID) {
		rc := roomChannel{Channel: c, Current: sh.Member == "" && c.ID == sh.Channel}
		for _, t := range all {
			if usesChannel(t, c.ID) {
				rc.Tasks++
			}
		}
		switch {
		case !c.Usable && !rc.Current:
			sh.Locked = append(sh.Locked, rc)
		case c.Voice:
			sh.Voice = append(sh.Voice, rc)
		default:
			sh.Text = append(sh.Text, rc)
		}
	}
	if p.Members {
		sh.Members = true
		sh.MQuery = strings.TrimSpace(q.Get("mq"))
		sh.MPage, _ = strconv.Atoi(q.Get("mp"))
		sh.MPage = max(sh.MPage, 1)
		sh.Presence = s.bot.Status().PresenceIntent
		sh.MGroups, sh.MHidden, sh.MMore = memberList(s.bot.Members(g.ID), sh.MQuery, sh.MPage, sh.Presence)
	}
	return sh
}

// serverOf is the server a page under a server shows: the one in the path,
// else ?server, else the user's first.
func (s *Server) serverOf(r *http.Request) (bot.Guild, bool) {
	sess := sessionFrom(r)
	for _, id := range []string{r.PathValue("id"), r.URL.Query().Get("server")} {
		if g, ok := s.bot.Guild(id); ok && sess.CanGuild(g.ID) {
			return g, true
		}
	}
	if gs := s.guilds(r); len(gs) > 0 {
		return gs[0], true
	}
	return bot.Guild{}, false
}
