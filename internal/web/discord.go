package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/oauth2"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
)

const (
	discordStateCookie = "__Host-discord-state"
	discordIssuer      = "https://discord.com"
	discordAPIBase     = "https://discord.com/api/v10"
	discordRevokeURL   = "https://discord.com/api/oauth2/token/revoke"
	discordCDN         = "https://cdn.discordapp.com"
	maxNoteRunes       = 300
)

var discordEndpoint = oauth2.Endpoint{
	AuthURL:   "https://discord.com/oauth2/authorize",
	TokenURL:  "https://discord.com/api/oauth2/token",
	AuthStyle: oauth2.AuthStyleInHeader,
}

type discordUserInfo struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
	Avatar     string `json:"avatar"`
}

// discordAPI is the part of Discord's OAuth2 API the login uses, a fake in tests.
type discordAPI interface {
	Exchange(ctx context.Context, oc *oauth2.Config, code, verifier string) (*oauth2.Token, error)
	Me(ctx context.Context, tok *oauth2.Token) (discordUserInfo, error)
	MyGuilds(ctx context.Context, tok *oauth2.Token) ([]string, error)
	Revoke(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token) error
}

type httpDiscord struct{ client *http.Client }

func (h httpDiscord) Exchange(ctx context.Context, oc *oauth2.Config, code, verifier string) (*oauth2.Token, error) {
	return oc.Exchange(context.WithValue(ctx, oauth2.HTTPClient, h.client), code, oauth2.VerifierOption(verifier))
}

func (h httpDiscord) get(ctx context.Context, tok *oauth2.Token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discordAPIBase+path, nil)
	if err != nil {
		return err
	}
	tok.SetAuthHeader(req)
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func (h httpDiscord) Me(ctx context.Context, tok *oauth2.Token) (discordUserInfo, error) {
	var u discordUserInfo
	err := h.get(ctx, tok, "/users/@me", &u)
	return u, err
}

func (h httpDiscord) MyGuilds(ctx context.Context, tok *oauth2.Token) ([]string, error) {
	var list []struct {
		ID string `json:"id"`
	}
	if err := h.get(ctx, tok, "/users/@me/guilds", &list); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, g := range list {
		ids = append(ids, g.ID)
	}
	return ids, nil
}

func (h httpDiscord) Revoke(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token) error {
	form := url.Values{"token": {tok.AccessToken}, "token_type_hint": {"access_token"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, discordRevokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(oc.ClientID), url.QueryEscape(oc.ClientSecret))
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("revoke: %s", resp.Status)
	}
	return nil
}

func (s *Server) discordEnabled() bool { return s.cfg.DiscordSecret != "" }

// discordConfig uses the bot's application as OAuth2 client, it is known once the bot connected.
func (s *Server) discordConfig() (*oauth2.Config, bool) {
	id := s.bot.Status().ApplicationID
	if !s.discordEnabled() || id == "" {
		return nil, false
	}
	return &oauth2.Config{
		ClientID:     id,
		ClientSecret: s.cfg.DiscordSecret,
		Endpoint:     discordEndpoint,
		RedirectURL:  s.cfg.Origin + "/auth/discord/callback",
		Scopes:       []string{"identify", "guilds"},
	}, true
}

func discordAuthURL(oc *oauth2.Config, state, verifier string) string {
	return oc.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
}

func (s *Server) discordLogin(w http.ResponseWriter, r *http.Request) {
	if !s.discordEnabled() {
		s.notFound(w, r)
		return
	}
	oc, ok := s.discordConfig()
	if !ok {
		s.fail(w, r, http.StatusServiceUnavailable, "Discord login works once the bot is connected to Discord. Try again in a moment.")
		return
	}
	state, verifier := randomToken(), oauth2.GenerateVerifier()
	if err := s.store.SaveOIDCLogin(r.Context(), store.ProviderDiscord, state, verifier, ""); err != nil {
		s.internalError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: discordStateCookie, Value: state, Path: "/", MaxAge: 600, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, discordAuthURL(oc, state, verifier), http.StatusFound)
}

// checkDiscordCallback returns a message for the user when the callback does
// not belong to a login started in this browser.
func checkDiscordCallback(q url.Values, cookieState string) string {
	state := q.Get("state")
	if state == "" || cookieState == "" || subtle.ConstantTimeCompare([]byte(cookieState), []byte(state)) != 1 {
		return "Login expired or was started in another browser. Please log in again."
	}
	// RFC 9207, a response from another authorization server must not be used
	if iss := q.Get("iss"); iss != "" && iss != discordIssuer {
		return "Login failed."
	}
	return ""
}

// discordIdentity redeems the code, reads the user and their servers and
// revokes the token right away. No token is kept.
func discordIdentity(ctx context.Context, api discordAPI, oc *oauth2.Config, code, verifier string, log *slog.Logger) (discordUserInfo, []string, error) {
	tok, err := api.Exchange(ctx, oc, code, verifier)
	if err != nil {
		return discordUserInfo{}, nil, err
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := api.Revoke(rctx, oc, tok); err != nil {
			log.Warn("revoking the discord token", "err", err)
		}
	}()
	me, err := api.Me(ctx, tok)
	if err != nil {
		return me, nil, err
	}
	if _, ok := bot.ParseID(me.ID); !ok || me.Username == "" {
		return me, nil, errors.New("discord returned no user")
	}
	guilds, err := api.MyGuilds(ctx, tok)
	return me, guilds, err
}

// sharedGuilds keeps the user's servers the bot is in, in the bot's order.
func sharedGuilds(userGuilds []string, botGuilds []bot.Guild) []string {
	out := []string{}
	for _, g := range botGuilds {
		if slices.Contains(userGuilds, g.ID) {
			out = append(out, g.ID)
		}
	}
	return out
}

func discordSession(u store.DiscordUser, shared []string) store.Session {
	return store.Session{
		Username:    store.DiscordSubject(u.ID),
		DisplayName: u.Name(),
		Provider:    store.ProviderDiscord,
		GuildIDs:    shared,
		CSRFToken:   randomToken(),
	}
}

func discordID(sess *store.Session) string { return strings.TrimPrefix(sess.Username, "discord:") }

func (s *Server) discordCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clearCookie(w, discordStateCookie)
	q := r.URL.Query()
	cookieState := ""
	if c, err := r.Cookie(discordStateCookie); err == nil {
		cookieState = c.Value
	}
	if msg := checkDiscordCallback(q, cookieState); msg != "" {
		s.fail(w, r, http.StatusBadRequest, msg)
		return
	}
	verifier, _, err := s.store.TakeOIDCLogin(ctx, store.ProviderDiscord, q.Get("state"))
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusBadRequest, "Login expired. Please log in again.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if q.Get("error") != "" {
		s.fail(w, r, http.StatusForbidden, "Discord login was cancelled.")
		return
	}
	oc, ok := s.discordConfig()
	if !ok {
		s.fail(w, r, http.StatusServiceUnavailable, "Discord login works once the bot is connected to Discord. Try again in a moment.")
		return
	}
	info, guilds, err := discordIdentity(ctx, s.discord, oc, q.Get("code"), verifier, s.log)
	if err != nil {
		s.log.Warn("discord login failed", "err", err)
		s.fail(w, r, http.StatusForbidden, "Discord login failed.")
		return
	}
	u := store.DiscordUser{ID: info.ID, Username: info.Username, GlobalName: info.GlobalName, Avatar: info.Avatar}
	blocked, err := s.store.UpsertDiscordUser(ctx, u)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	actor := store.DiscordSubject(u.ID)
	if blocked {
		s.auditAs(ctx, actor, "access.login", "@"+u.Username, "blocked, no session")
		s.render(w, r, http.StatusForbidden, "access", accessPage{View: "blocked"})
		return
	}
	shared := sharedGuilds(guilds, s.bot.Guilds())
	sess := discordSession(u, shared)
	token := randomToken()
	if err := s.store.CreateSession(ctx, token, sess); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.auditAs(ctx, actor, "access.login", "@"+u.Username, fmt.Sprintf("%d shared servers", len(shared)))
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", MaxAge: int(maxSession.Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusFound)
}

// discordStart is all a Discord user without an approved server may reach.
var discordStart = map[string]bool{
	"GET /{$}":              true,
	"POST /logout":          true,
	"POST /account":         true,
	"POST /access/request":  true,
	"POST /access/withdraw": true,
}

// discordRoutes are the routes of approved Discord users, keyed by mux pattern.
// Each handler limits the tasks, runs and servers it shows and changes to the
// user's servers. Media and messages are open to every user.
var discordRoutes = map[string]bool{
	"GET /servers":                    true,
	"GET /servers/{id}":               true,
	"POST /servers/order":             true,
	"GET /members":                    true,
	"POST /servers/{id}/stop":         true,
	"POST /servers/{id}/leave":        true,
	"POST /servers/{id}/play":         true,
	"POST /servers/{id}/join":         true,
	"GET /events":                     true,
	"GET /room":                       true,
	"GET /room/{id}":                  true,
	"POST /room/{id}/send":            true,
	"GET /tasks":                      true,
	"GET /tasks/new":                  true,
	"POST /tasks/new":                 true,
	"GET /tasks/{id}":                 true,
	"POST /tasks/{id}":                true,
	"POST /tasks/{id}/run":            true,
	"POST /tasks/{id}/pause":          true,
	"POST /tasks/{id}/resume":         true,
	"POST /tasks/{id}/duplicate":      true,
	"POST /tasks/{id}/delete":         true,
	"POST /runs/{id}/stop":            true,
	"GET /messages":                   true,
	"GET /messages/new":               true,
	"POST /messages/new":              true,
	"GET /messages/{id}":              true,
	"POST /messages/{id}":             true,
	"POST /messages/{id}/send":        true,
	"POST /messages/{id}/duplicate":   true,
	"POST /messages/{id}/delete":      true,
	"GET /media":                      true,
	"POST /media":                     true,
	"POST /media/url":                 true,
	"GET /media/{id}/file":            true,
	"GET /sounds":                     true,
	"GET /tags":                       true,
	"POST /tags/look":                 true,
	"POST /prefs/preview-volume":      true,
	"POST /prefs/play-volume":         true,
	"POST /media/{id}":                true,
	"POST /media/{id}/play":           true,
	"POST /media/{id}/delete":         true,
	"POST /media/group":               true,
	"POST /media/{id}/sound":          true,
	"POST /media/{id}/favorite":       true,
	"GET /trash":                      true,
	"POST /trash/{kind}/{id}/restore": true,
}

// discordAllowed checks the route of a Discord session, r.Pattern is set by the ServeMux.
func discordAllowed(r *http.Request, sess *store.Session) bool {
	if discordStart[r.Pattern] {
		return true
	}
	return !sess.NoAccess() && discordRoutes[r.Pattern]
}

// grantedGuilds keeps the approved servers the user was still in at login.
func grantedGuilds(grants, member []string) []string {
	var out []string
	for _, g := range grants {
		if slices.Contains(member, g) {
			out = append(out, g)
		}
	}
	return out
}

// isAdmin is recomputed on every request. BOT_ADMINS and BOT_ADMIN_GROUP name
// Cloudron users, a Discord account is never admin, whatever it is called.
func (s *Server) isAdmin(sess *store.Session) bool {
	return !sess.Discord() && s.cfg.Admins.IsAdmin(sess.Username, sess.Groups)
}

type accessPage struct {
	View      string // approved, pending, denied, revoked, form, none or blocked
	User      store.DiscordUser
	Avatar    string
	Servers   []bot.Guild // choices of the request form
	Asked     []string    // server names of the open request
	ExpiresOn string
	Active    []string // names of the servers the user may manage
	NoteMax   int
}

// discordView picks what the start page shows a Discord user, and the servers
// they may manage: approved ones they still share with the bot.
func discordView(acc store.DiscordAccess, shared []string, again bool) (string, []string) {
	if acc.User.Blocked {
		return "blocked", nil
	}
	var active []string
	for _, g := range acc.Grants {
		if slices.Contains(shared, g) {
			active = append(active, g)
		}
	}
	switch {
	case len(active) > 0:
		return "approved", active
	case acc.Latest != nil && acc.Latest.Status == store.AccessPending:
		return "pending", nil
	case len(shared) == 0:
		return "none", nil
	case acc.Latest != nil && !again && (acc.Latest.Status == store.AccessDenied || acc.Latest.Status == store.AccessRevoked):
		return acc.Latest.Status, nil
	}
	return "form", nil
}

func avatarURL(u store.DiscordUser) string {
	if u.Avatar == "" || strings.Trim(u.Avatar, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return ""
	}
	return discordCDN + "/avatars/" + u.ID + "/" + u.Avatar + ".png?size=64"
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if sessionFrom(r).NoAccess() {
		s.discordHome(w, r)
		return
	}
	s.dashboard(w, r)
}

func (s *Server) discordHome(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	acc, err := s.store.DiscordAccess(r.Context(), discordID(sess))
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	view, active := discordView(acc, sess.GuildIDs, r.URL.Query().Has("again"))
	d := accessPage{View: view, User: acc.User, Avatar: avatarURL(acc.User), NoteMax: maxNoteRunes}
	for _, id := range active {
		d.Active = append(d.Active, s.bot.GuildName(id))
	}
	switch view {
	case "form":
		for _, id := range sess.GuildIDs {
			if g, ok := s.bot.Guild(id); ok {
				d.Servers = append(d.Servers, g)
			}
		}
	case "pending":
		for _, id := range acc.Latest.GuildIDs {
			d.Asked = append(d.Asked, s.bot.GuildName(id))
		}
		d.ExpiresOn = day(acc.Latest.ExpiresAt)
	}
	s.render(w, r, http.StatusOK, "access", d)
}

// pickedGuilds keeps the posted servers that are allowed, each once.
func pickedGuilds(posted, allowed []string) []string {
	var out []string
	for _, id := range posted {
		if slices.Contains(allowed, id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) requestAccess(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	picked := pickedGuilds(r.PostForm["guild"], sess.GuildIDs)
	if len(picked) == 0 {
		s.fail(w, r, http.StatusBadRequest, "Pick at least one server.")
		return
	}
	note := strings.TrimSpace(strings.ReplaceAll(r.PostForm.Get("note"), "\r\n", "\n"))
	if utf8.RuneCountInString(note) > maxNoteRunes {
		s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("The message to the admin can have up to %d characters.", maxNoteRunes))
		return
	}
	_, err := s.store.CreateAccessRequest(r.Context(), discordID(sess), picked, note)
	switch {
	case errors.Is(err, store.ErrPending):
		redirect(w, r, "/", "")
		return
	case errors.Is(err, store.ErrBlocked):
		s.render(w, r, http.StatusForbidden, "access", accessPage{View: "blocked"})
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	names := make([]string, 0, len(picked))
	for _, id := range picked {
		names = append(names, s.bot.GuildName(id))
	}
	s.audit(r, "access.request", sess.DisplayName, strings.Join(names, ", "))
	redirect(w, r, "/", "requested")
}

func (s *Server) withdrawAccess(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	err := s.store.WithdrawAccessRequest(r.Context(), discordID(sess))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internalError(w, r, err)
		return
	}
	if err == nil {
		s.audit(r, "access.withdraw", sess.DisplayName, "")
	}
	redirect(w, r, "/", "withdrawn")
}
