package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/BrutalBirdie/pts-discord-bot/internal/access"
	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
)

func discordTestConfig() *oauth2.Config {
	return &oauth2.Config{ClientID: "42", ClientSecret: "s3cret", Endpoint: discordEndpoint, RedirectURL: "https://bot.example/auth/discord/callback", Scopes: []string{"identify", "guilds"}}
}

func TestDiscordAuthURL(t *testing.T) {
	verifier := oauth2.GenerateVerifier()
	u, err := url.Parse(discordAuthURL(discordTestConfig(), "st4te", verifier))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "discord.com" || u.Path != "/oauth2/authorize" {
		t.Errorf("endpoint: %s", u)
	}
	want := map[string]string{"client_id": "42", "state": "st4te", "response_type": "code", "scope": "identify guilds",
		"redirect_uri": "https://bot.example/auth/discord/callback", "code_challenge_method": "S256", "code_challenge": oauth2.S256ChallengeFromVerifier(verifier)}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if strings.Contains(u.String(), verifier) || strings.Contains(u.String(), "s3cret") {
		t.Error("verifier or secret in the authorize URL")
	}
	if discordEndpoint.AuthStyle != oauth2.AuthStyleInHeader || discordEndpoint.TokenURL != "https://discord.com/api/oauth2/token" {
		t.Errorf("token endpoint: %+v", discordEndpoint)
	}
}

func TestCheckDiscordCallback(t *testing.T) {
	cases := []struct {
		query, cookie string
		ok            bool
	}{
		{"state=abc&code=x", "abc", true},
		{"state=abc&code=x&iss=https%3A%2F%2Fdiscord.com", "abc", true},
		{"state=abc&code=x&iss=https%3A%2F%2Fevil.example", "abc", false},
		{"state=abc&code=x", "abd", false},
		{"state=abc&code=x", "", false},
		{"code=x", "", false},
		{"state=&code=x", "", false},
	}
	for _, c := range cases {
		q, _ := url.ParseQuery(c.query)
		if got := checkDiscordCallback(q, c.cookie) == ""; got != c.ok {
			t.Errorf("%s with cookie %q: ok=%v", c.query, c.cookie, got)
		}
	}
}

type fakeDiscord struct {
	meErr    error
	revoked  []string
	verifier string
}

func (f *fakeDiscord) Exchange(_ context.Context, _ *oauth2.Config, code, verifier string) (*oauth2.Token, error) {
	if code != "good" {
		return nil, errors.New("invalid_grant")
	}
	f.verifier = verifier
	return &oauth2.Token{AccessToken: "at-1", TokenType: "Bearer"}, nil
}

func (f *fakeDiscord) Me(context.Context, *oauth2.Token) (discordUserInfo, error) {
	return discordUserInfo{ID: "111111111111111111", Username: "ann", GlobalName: "Ann"}, f.meErr
}

func (f *fakeDiscord) MyGuilds(context.Context, *oauth2.Token) ([]string, error) {
	return []string{"1", "2", "3"}, nil
}

func (f *fakeDiscord) Revoke(_ context.Context, _ *oauth2.Config, tok *oauth2.Token) error {
	f.revoked = append(f.revoked, tok.AccessToken)
	return nil
}

func TestDiscordIdentityRevokesTheToken(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &fakeDiscord{}
	me, guilds, err := discordIdentity(context.Background(), f, discordTestConfig(), "good", "v3rifier", log)
	if err != nil || me.ID != "111111111111111111" || len(guilds) != 3 || f.verifier != "v3rifier" {
		t.Fatalf("identity: %+v %v %v", me, guilds, err)
	}
	if !slices.Equal(f.revoked, []string{"at-1"}) {
		t.Errorf("token not revoked: %v", f.revoked)
	}
	f = &fakeDiscord{meErr: errors.New("401")}
	if _, _, err := discordIdentity(context.Background(), f, discordTestConfig(), "good", "v", log); err == nil || len(f.revoked) != 1 {
		t.Errorf("revoke after a failed lookup: %v %v", err, f.revoked)
	}
	f = &fakeDiscord{}
	if _, _, err := discordIdentity(context.Background(), f, discordTestConfig(), "bad", "v", log); err == nil || len(f.revoked) != 0 {
		t.Errorf("bad code: %v %v", err, f.revoked)
	}
}

func TestSharedGuilds(t *testing.T) {
	got := sharedGuilds([]string{"3", "9", "1"}, []bot.Guild{{ID: "1"}, {ID: "2"}, {ID: "3"}})
	if !slices.Equal(got, []string{"1", "3"}) {
		t.Errorf("shared: %v", got)
	}
	if got := sharedGuilds(nil, nil); got == nil || len(got) != 0 {
		t.Errorf("empty: %#v", got)
	}
}

// A Discord account named like a BOT_ADMINS entry must not become admin.
func TestDiscordUserIsNeverAdmin(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(Config{Origin: "https://bot.example", Admins: access.Parse("ann discord:111111111111111111", "bot-admins")}, nil, bot.New(bot.Config{Logger: log}), nil, log)
	if err != nil {
		t.Fatal(err)
	}
	sess := discordSession(store.DiscordUser{ID: "111111111111111111", Username: "ann", GlobalName: "ann"}, []string{"1"})
	if sess.Username != "discord:111111111111111111" || sess.Provider != store.ProviderDiscord || sess.IsAdmin || len(sess.Groups) != 0 {
		t.Fatalf("session: %+v", sess)
	}
	if s.isAdmin(&sess) {
		t.Error("discord session is admin")
	}
	spoofed := store.Session{Username: "ann", Provider: store.ProviderDiscord, Groups: []string{"bot-admins"}}
	if s.isAdmin(&spoofed) {
		t.Error("discord session with an admin username is admin")
	}
	if cloudron := (store.Session{Username: "ann", Provider: store.ProviderCloudron}); !s.isAdmin(&cloudron) {
		t.Error("cloudron admin lost admin")
	}
}

func TestDiscordRoutes(t *testing.T) {
	mux := testServer(t).routes()
	pending := &store.Session{Username: "discord:9", Provider: store.ProviderDiscord, GuildIDs: []string{"1"}}
	approved := &store.Session{Username: "discord:9", Provider: store.ProviderDiscord, GuildIDs: []string{"1"}, Granted: []string{"1"}}
	for _, c := range []struct {
		method, target string
		pending, ok    bool
	}{
		{"GET", "/", true, true}, {"HEAD", "/", true, true}, {"GET", "/?again=1", true, true}, {"POST", "/logout", true, true}, {"POST", "/account", true, true},
		{"POST", "/access/request", true, true}, {"POST", "/access/withdraw", true, true},
		{"GET", "/tasks", false, true}, {"GET", "/servers/1", false, true}, {"GET", "/media/1/file", false, true}, {"GET", "/members", false, true},
		{"GET", "/trash", false, true}, {"POST", "/trash/task/1/restore", false, true}, {"POST", "/media", false, true}, {"POST", "/servers/1/play", false, true},
		{"GET", "/admin", false, false}, {"GET", "/admin/access", false, false}, {"POST", "/admin/access/1", false, false}, {"POST", "/admin/settings", false, false},
		{"POST", "/trash/task/1/purge", false, false}, {"POST", "/trash/empty", false, false}, {"POST", "/servers/refresh", false, false}, {"POST", "/", false, false},
	} {
		r := httptest.NewRequest(c.method, c.target, nil)
		_, r.Pattern = mux.Handler(r)
		if got := discordAllowed(r, pending); got != c.pending {
			t.Errorf("pending %s %s (%s): allowed=%v", c.method, c.target, r.Pattern, got)
		}
		if got := discordAllowed(r, approved); got != c.ok {
			t.Errorf("approved %s %s (%s): allowed=%v", c.method, c.target, r.Pattern, got)
		}
	}
	for pattern := range discordRoutes {
		method, path, _ := strings.Cut(pattern, " ")
		path = strings.NewReplacer("{id}", "1", "{kind}", "task").Replace(path)
		if _, got := mux.Handler(httptest.NewRequest(method, path, nil)); got != pattern {
			t.Errorf("allowlisted %s is not a route (matched %q)", pattern, got)
		}
	}
}

func TestGrantedGuilds(t *testing.T) {
	if got := grantedGuilds([]string{"1", "2", "3"}, []string{"3", "1", "9"}); !slices.Equal(got, []string{"1", "3"}) {
		t.Errorf("granted: %v", got)
	}
	if got := grantedGuilds([]string{"1"}, nil); len(got) != 0 {
		t.Errorf("left server still granted: %v", got)
	}
}

func TestDiscordView(t *testing.T) {
	req := func(status string) *store.AccessRequest {
		return &store.AccessRequest{Status: status, GuildIDs: []string{"1"}}
	}
	shared := []string{"1", "2"}
	cases := []struct {
		name   string
		acc    store.DiscordAccess
		shared []string
		again  bool
		view   string
	}{
		{"blocked wins", store.DiscordAccess{User: store.DiscordUser{Blocked: true}, Grants: []string{"1"}}, shared, false, "blocked"},
		{"granted", store.DiscordAccess{Grants: []string{"2", "7"}, Latest: req(store.AccessApproved)}, shared, false, "approved"},
		{"grant on a server left", store.DiscordAccess{Grants: []string{"7"}, Latest: req(store.AccessApproved)}, shared, false, "form"},
		{"pending", store.DiscordAccess{Latest: req(store.AccessPending)}, shared, false, "pending"},
		{"pending without shared servers", store.DiscordAccess{Latest: req(store.AccessPending)}, nil, false, "pending"},
		{"denied", store.DiscordAccess{Latest: req(store.AccessDenied)}, shared, false, "denied"},
		{"revoked", store.DiscordAccess{Latest: req(store.AccessRevoked)}, shared, false, "revoked"},
		{"ask again", store.DiscordAccess{Latest: req(store.AccessDenied)}, shared, true, "form"},
		{"first time", store.DiscordAccess{}, shared, false, "form"},
		{"no shared server", store.DiscordAccess{}, nil, false, "none"},
	}
	for _, c := range cases {
		view, active := discordView(c.acc, c.shared, c.again)
		if view != c.view {
			t.Errorf("%s: %s, want %s", c.name, view, c.view)
		}
		if view == "approved" && !slices.Equal(active, []string{"2"}) {
			t.Errorf("%s: active %v", c.name, active)
		}
	}
}

func TestPickedGuildsAndAccessURL(t *testing.T) {
	if got := pickedGuilds([]string{"2", "9", "2", "1"}, []string{"1", "2"}); !slices.Equal(got, []string{"2", "1"}) {
		t.Errorf("picked: %v", got)
	}
	for in, want := range map[[3]string]string{
		{"denied", "mira", "2"}:   "/admin/access?page=2&q=mira&state=denied",
		{"bogus", "", "1"}:        "/admin/access",
		{"pending", " a&b ", "x"}: "/admin/access?q=a%26b&state=pending",
	} {
		if got := accessURL(in[0], in[1], in[2]); got != want {
			t.Errorf("%v: %s", in, got)
		}
	}
}

type fakeKeeper struct {
	expired []store.AccessRequest
	grants  []store.Grant
	removed []store.Grant
}

func (f *fakeKeeper) AutoDenyExpired(context.Context) ([]store.AccessRequest, error) {
	return f.expired, nil
}
func (f *fakeKeeper) AllGrants(context.Context) ([]store.Grant, error) { return f.grants, nil }
func (f *fakeKeeper) RemoveGrant(_ context.Context, u, g string) error {
	f.removed = append(f.removed, store.Grant{UserID: u, GuildID: g})
	return nil
}

type fakeMembers map[store.Grant]error

func (m fakeMembers) IsMember(_ context.Context, g, u string) (bool, error) {
	err, listed := m[store.Grant{UserID: u, GuildID: g}]
	return listed && err == nil, err
}

func TestMaintainAccess(t *testing.T) {
	k := &fakeKeeper{
		expired: []store.AccessRequest{{ID: 5, UserID: "7"}},
		grants:  []store.Grant{{UserID: "1", GuildID: "10"}, {UserID: "1", GuildID: "11"}, {UserID: "2", GuildID: "10"}},
	}
	members := fakeMembers{{UserID: "1", GuildID: "10"}: nil, {UserID: "2", GuildID: "10"}: errors.New("discord down")}
	var audits []string
	err := maintainAccess(context.Background(), k, members, func(action, target, detail string) { audits = append(audits, action+" "+target) })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(k.removed, []store.Grant{{UserID: "1", GuildID: "11"}}) {
		t.Errorf("removed %v, an unknown answer must keep the grant", k.removed)
	}
	if !slices.Equal(audits, []string{"access.autodeny discord:7", "access.leave discord:1"}) {
		t.Errorf("audit %v", audits)
	}
}

func TestNewAccessRow(t *testing.T) {
	SetDisplayTimezone("UTC")
	now := time.Now().UTC()
	name := func(id string) string { return map[string]string{"1": "PTS Gaming", "2": "PTS Lounge"}[id] }
	decided := now.Add(-time.Hour)
	u := store.DiscordUser{ID: "9", Username: "mira.k", GlobalName: "Mira", Avatar: "a_1f"}
	pending := newAccessRow(store.AccessEntry{User: u, State: store.AccessPending, Request: &store.AccessRequest{GuildIDs: []string{"1", "2"}, Note: "raids",
		RequestedAt: now.Add(-6 * 24 * time.Hour), ExpiresAt: now.Add(24 * time.Hour)}}, name, now)
	if pending.Chip != "denied tomorrow" || pending.ChipClass != "warn" || !pending.Editable || len(pending.Servers) != 2 || pending.Servers[1].Name != "PTS Lounge" ||
		pending.Note != "raids" || pending.Sub != "Requested 6 days ago" || pending.Avatar != discordCDN+"/avatars/9/a_1f.png?size=64" {
		t.Errorf("pending: %+v", pending)
	}
	auto := newAccessRow(store.AccessEntry{User: u, State: store.AccessDenied, Request: &store.AccessRequest{Status: store.AccessDenied, DecidedBy: store.SystemActor, DecidedAt: &decided}}, name, now)
	if !strings.HasPrefix(auto.Sub, "Denied automatically on ") || !strings.HasSuffix(auto.Sub, "nobody answered in 7 days. Can ask again.") {
		t.Errorf("auto deny: %q", auto.Sub)
	}
	revoked := newAccessRow(store.AccessEntry{User: u, State: store.AccessDenied, Request: &store.AccessRequest{Status: store.AccessRevoked, DecidedBy: "ann", DecidedAt: &decided}}, name, now)
	if !strings.HasPrefix(revoked.Sub, "Access revoked by ann on ") {
		t.Errorf("revoked: %q", revoked.Sub)
	}
	approved := newAccessRow(store.AccessEntry{User: u, State: store.AccessApproved, Grants: []string{"1"}, GrantedBy: "ann", GrantedAt: &decided, Request: &store.AccessRequest{Note: "old"}}, name, now)
	if approved.ServersLabel != "Can manage" || approved.Editable || approved.Note != "" || approved.ChipClass != "ok" {
		t.Errorf("approved: %+v", approved)
	}
	if avatarURL(store.DiscordUser{ID: "9", Avatar: "../x"}) != "" {
		t.Error("avatar hash not checked")
	}
}

func renderAs(t *testing.T, s *Server, sess *store.Session, page string, data any) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/", nil)
	if sess != nil {
		req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, sess))
	}
	rec := httptest.NewRecorder()
	s.render(rec, req, http.StatusOK, page, data)
	body := rec.Body.String()
	if !strings.Contains(body, "</html>") {
		t.Fatalf("%s: incomplete page:\n%s", page, body)
	}
	if strings.Contains(body, " style=") {
		t.Errorf("%s: inline style attribute, blocked by the CSP", page)
	}
	if strings.Contains(body, "\u2014") {
		t.Errorf("%s: em dash", page)
	}
	return body
}

func TestLoginShowsDiscordOnlyWithSecret(t *testing.T) {
	s := testServer(t)
	if body := renderAs(t, s, nil, "login", nil); strings.Contains(body, "/login/discord") || !strings.Contains(body, "Log in with Cloudron") {
		t.Error("discord button without a secret")
	}
	s.cfg.DiscordSecret = "x"
	body := renderAs(t, s, nil, "login", nil)
	for _, want := range []string{`href="/login/discord"`, "Log in with Discord", "An admin approves new members first."} {
		if !strings.Contains(body, want) {
			t.Errorf("login: missing %q", want)
		}
	}
}

func TestAccessPageStates(t *testing.T) {
	s := testServer(t)
	sess := &store.Session{Username: "discord:9", DisplayName: "Mira", Provider: store.ProviderDiscord, CSRFToken: "tok"}
	u := store.DiscordUser{ID: "9", Username: "mira.k", GlobalName: "Mira"}
	for _, c := range []struct {
		data    accessPage
		want    []string
		without []string
	}{
		{accessPage{View: "form", User: u, NoteMax: 300, Servers: []bot.Guild{{ID: "1", Name: "PTS Gaming", MemberCount: 1204}, {ID: "2", Name: "PTS Lounge", MemberCount: 1}}},
			[]string{"Ask for access", `action="/access/request"`, `name="guild" value="1"`, "1204 members", "1 member<", `maxlength="300"`, "@mira.k on Discord", `<span class="ava lg" aria-hidden="true">M</span>`, "Send request", `data-pick-list`}, nil},
		{accessPage{View: "pending", User: u, Asked: []string{"PTS Gaming", "PTS Lounge"}, ExpiresOn: "13 Nov"},
			[]string{"Request sent", "PTS Gaming and PTS Lounge", "denied on 13 Nov", `action="/access/withdraw"`}, []string{`action="/access/request"`}},
		{accessPage{View: "denied", User: u}, []string{"Request denied", `href="/?again=1"`}, nil},
		{accessPage{View: "revoked", User: u}, []string{"Access removed", `href="/?again=1"`}, nil},
		{accessPage{View: "none", User: u}, []string{"No shared server"}, []string{`href="/?again=1"`}},
		{accessPage{View: "approved", User: u, Active: []string{"PTS Gaming"}}, []string{"Access approved for: PTS Gaming.", "Open the dashboard"}, nil},
	} {
		body := renderAs(t, s, sess, "access", c.data)
		for _, w := range c.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: missing %q", c.data.View, w)
			}
		}
		for _, w := range append(c.without, `href="/tasks"`, `href="/trash"`, `href="/admin"`, "administrator", "The bot is not connected") {
			if strings.Contains(body, w) {
				t.Errorf("%s: should not contain %q", c.data.View, w)
			}
		}
	}
	body := renderAs(t, s, nil, "access", accessPage{View: "blocked"})
	if !strings.Contains(body, "No access") || !strings.Contains(body, "cannot ask for access") || strings.Contains(body, "/logout") {
		t.Errorf("blocked page:\n%s", body)
	}
}

func TestAccessTabRenders(t *testing.T) {
	s := testServer(t)
	admin := &store.Session{Username: "ann", DisplayName: "Ann", IsAdmin: true, CSRFToken: "tok"}
	tab := &accessTab{State: "pending", Counts: map[string]int{"pending": 2, "blocked": 1}, Page: 1, Pages: 2, Total: 26, Search: "pts",
		Rows: []accessRow{
			{ID: "9", Name: "Mira", Username: "mira.k", State: "pending", Chip: "denied in 5 days", Sub: "Requested 2 days ago", Note: "raid nights",
				ServersLabel: "Asks for", Editable: true, Servers: []accessServer{{ID: "1", Name: "PTS Gaming"}}, Avatar: discordCDN + "/avatars/9/abc.png?size=64"},
			{ID: "8", Name: "Lee", Username: "lee.w", State: "approved", Chip: "approved", ChipClass: "ok", ServersLabel: "Can manage", Servers: []accessServer{{ID: "1", Name: "PTS Gaming"}}},
			{ID: "7", Name: "spam", Username: "free_nitro_x", State: "blocked", Chip: "blocked", ChipClass: "danger"},
		}}
	body := renderAs(t, s, admin, "admin", adminData{Tab: "access", Access: tab})
	for _, want := range []string{
		`href="/admin/access" aria-current="page"`, "Pending 2", "Blocked 1", "Approved 0", `value="pts"`,
		`action="/admin/access/9" id="access-9"`, `name="guild" value="1" form="access-9" checked`, `name="do" value="approve"`, `name="do" value="deny"`,
		`value="block" class="ib danger" aria-label="Block Mira" data-tip="Block" data-confirm-desc="Denies the request and stops @mira.k from asking again until you unblock them." data-irreversible`,
		`value="revoke"`, `data-confirm-desc="Logs Lee out and removes their access." data-irreversible`, `value="unblock"`,
		`<blockquote class="note">raid nights</blockquote>`, `<img src="https://cdn.discordapp.com/avatars/9/abc.png?size=64"`, "Page 1 of 2", "26 requests",
		`href="/admin/access?state=pending&amp;q=pts&amp;page=2"`, `<span class="lbl">Approve</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("access tab: missing %q", want)
		}
	}
	if strings.Count(body, `name="do" value="unblock"`) != 1 || strings.Count(body, `value="block"`) != 2 {
		t.Error("wrong actions per state")
	}
	empty := renderAs(t, s, admin, "admin", adminData{Tab: "access", Access: &accessTab{State: "denied", Counts: map[string]int{}, Page: 1, Pages: 1}})
	if !strings.Contains(empty, "No denied requests.") || strings.Contains(empty, "Page 1 of") {
		t.Error("empty denied tab")
	}
}

func TestPendingBadgeAndCSP(t *testing.T) {
	s := testServer(t)
	admin := &store.Session{Username: "ann", DisplayName: "Ann", IsAdmin: true, CSRFToken: "tok"}
	var b strings.Builder
	if err := s.pages["admin"].Execute(&b, pageData{Session: admin, Nav: "admin", Pending: 3, Data: adminData{Tab: "overview"}}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(b.String(), `<span class="badge accent" aria-label="3 open`); n != 2 {
		t.Errorf("pending badge on nav and tab: %d", n)
	}
	rec := httptest.NewRecorder()
	securityHeaders(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' https://cdn.discordapp.com;") {
		t.Errorf("csp: %s", csp)
	}
}

func TestPagesForApprovedDiscordUser(t *testing.T) {
	s := testServer(t)
	sess := &store.Session{Username: "discord:9", DisplayName: "Mira", Provider: store.ProviderDiscord, GuildIDs: []string{"1", "2"}, Granted: []string{"1"}, CSRFToken: "tok"}
	cloudron := &store.Session{Username: "ann", DisplayName: "Ann", IsAdmin: true, CSRFToken: "tok"}
	guilds := []bot.Guild{{ID: "1", Name: "PTS Gaming"}}
	sound := store.Media{ID: 5, Name: "a.ogg", Kind: store.KindSound, GuildID: "1", GroupOnly: true, UsedBy: []string{"task Intro"}, UsedElse: 2}
	msg := messages.Message{ID: 7, Name: "Hello", GuildID: "1", Embeds: []messages.Embed{}}
	for _, c := range []struct {
		sess    *store.Session
		page    string
		data    any
		want    []string
		without []string
	}{
		{sess, "dashboard", dashboardData{}, []string{`href="/tasks"`, `href="/messages"`, `href="/media"`, `href="/trash"`, `href="/servers"`, "Overview"}, []string{`href="/admin"`, "administrator"}},
		{sess, "servers", serversData{Guilds: guilds}, []string{`href="/room/1"`}, []string{"/servers/refresh", "Open in Discord"}},
		{sess, "media", mediaData{Items: []store.Media{sound}, Counts: map[string]int{"": 1}, Groups: guilds, DefaultGroup: "1", Names: map[string]string{"1": "PTS Gaming"}},
			[]string{`<option value="1" selected>PTS Gaming</option>`, "used by task Intro, 2 tasks in other servers", `aria-label="Delete a.ogg" data-tip="Delete" disabled`, "PTS Gaming", "only this server", `action="/media/group"`},
			[]string{"Shared, for Cloudron users only", "/move"}},
		{cloudron, "media", mediaData{Counts: map[string]int{}, Groups: guilds},
			[]string{`<option value="">No server</option>`, `<option value="1">PTS Gaming</option>`, `name="group_only"`}, nil},
		{sess, "messages", messagesData{Rows: []messageRow{{Message: msg, Guild: "PTS Gaming", UsedElse: 1}}, Groups: guilds}, []string{"PTS Gaming", "1 task in other servers", `server=1"`}, nil},
		{sess, "message", messageEditor{Message: msg, Groups: guilds, ByID: map[int64]store.Media{}, Limits: map[string]int{"content": 2000, "embeds": 10, "embedTotal": 6000}},
			[]string{`<select name="guild" data-reload`, `<option value="1" selected>PTS Gaming</option>`, `<option value="">No server</option>`, `name="group_only"`}, []string{"Shared, for Cloudron users only", "/move"}},
		{sess, "trash", trashData{Counts: map[string]int{}}, []string{"Only admins can delete items for good"}, []string{"/purge", "/trash/empty"}},
	} {
		body := renderAs(t, s, c.sess, c.page, c.data)
		for _, w := range c.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s as %s: missing %q", c.page, c.sess.Username, w)
			}
		}
		for _, w := range c.without {
			if strings.Contains(body, w) {
				t.Errorf("%s as %s: should not contain %q", c.page, c.sess.Username, w)
			}
		}
	}
}
