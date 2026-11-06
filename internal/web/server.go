// Package web serves the control panel.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/BrutalBirdie/pts-discord-bot/internal/access"
	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/media"
	"github.com/BrutalBirdie/pts-discord-bot/internal/runner"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
)

//go:embed templates static
var assets embed.FS

const (
	sessionCookie = "__Host-session"
	stateCookie   = "__Host-oidc-state"
	idleTimeout   = 8 * time.Hour
	maxSession    = 24 * time.Hour
	maxBodyBytes  = 512 << 10
)

type Config struct {
	Origin       string // https://fqdn of the app
	Issuer       string
	ClientID     string
	ClientSecret string
	Admins       access.Admins
	MediaDir     media.Dir
	// DiscordSecret is the OAuth2 client secret of the bot's application, empty turns Discord login off
	DiscordSecret string
}

type Server struct {
	cfg   Config
	store *store.Store
	bot   *bot.Bot
	run   *runner.Runner
	log   *slog.Logger
	pages map[string]*template.Template

	oidcMu   sync.Mutex
	provider *oidc.Provider
	discord  discordAPI
}

var pageNames = []string{"login", "error", "dashboard", "servers", "server", "tasks", "task", "messages", "message", "media", "trash", "admin", "access"}

func New(cfg Config, st *store.Store, b *bot.Bot, r *runner.Runner, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, store: st, bot: b, run: r, log: log, pages: map[string]*template.Template{}, discord: httpDiscord{client: &http.Client{Timeout: 15 * time.Second}}}
	for _, page := range pageNames {
		t, err := template.New("layout.html").Funcs(viewFuncs).ParseFS(assets, "templates/layout.html", "templates/parts.html", "templates/icons.html", "templates/"+page+".html")
		if err != nil {
			return nil, err
		}
		s.pages[page] = t
	}
	b.OnGuildLeave(s.guildLeft)
	return s, nil
}

func (s *Server) Handler() http.Handler {
	static, _ := fs.Sub(assets, "static")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("GET /login/discord", s.discordLogin)
	mux.HandleFunc("GET /auth/discord/callback", s.discordCallback)
	mux.Handle("POST /access/request", s.requireDiscord(s.requestAccess))
	mux.Handle("POST /access/withdraw", s.requireDiscord(s.withdrawAccess))

	mux.Handle("GET /{$}", s.requireLogin(s.home))
	mux.Handle("POST /logout", s.requireLogin(s.logout))
	mux.Handle("POST /account", s.requireLogin(s.saveAccount))

	mux.Handle("GET /servers", s.requireLogin(s.servers))
	mux.Handle("POST /servers/refresh", s.requireLogin(s.refreshServers))
	mux.Handle("GET /servers/{id}", s.requireLogin(s.server))
	mux.Handle("GET /members", s.requireLogin(s.searchMembers))
	mux.Handle("POST /servers/{id}/stop", s.requireLogin(s.stopVoice))
	mux.Handle("POST /servers/{id}/leave", s.requireLogin(s.leaveVoice))
	mux.Handle("POST /servers/{id}/play", s.requireLogin(s.playInChannel))
	mux.Handle("POST /servers/{id}/join", s.requireLogin(s.joinVoice))

	mux.Handle("GET /tasks", s.requireLogin(s.tasks))
	mux.Handle("GET /tasks/new", s.requireLogin(s.newTask))
	mux.Handle("POST /tasks/new", s.requireLogin(s.postTask))
	mux.Handle("GET /tasks/{id}", s.requireLogin(s.task))
	mux.Handle("POST /tasks/{id}", s.requireLogin(s.postTask))
	mux.Handle("POST /tasks/{id}/run", s.requireLogin(s.runTask))
	mux.Handle("POST /tasks/{id}/pause", s.requireLogin(s.pauseTask))
	mux.Handle("POST /tasks/{id}/resume", s.requireLogin(s.resumeTask))
	mux.Handle("POST /tasks/{id}/duplicate", s.requireLogin(s.duplicateTask))
	mux.Handle("POST /tasks/{id}/delete", s.requireLogin(s.deleteTask))

	mux.Handle("GET /messages", s.requireLogin(s.messagesList))
	mux.Handle("GET /messages/new", s.requireLogin(s.newMessage))
	mux.Handle("POST /messages/new", s.requireLogin(s.postMessage))
	mux.Handle("GET /messages/{id}", s.requireLogin(s.message))
	mux.Handle("POST /messages/{id}", s.requireLogin(s.postMessage))
	mux.Handle("POST /messages/{id}/send", s.requireLogin(s.sendMessage))
	mux.Handle("POST /messages/{id}/duplicate", s.requireLogin(s.duplicateMessage))
	mux.Handle("POST /messages/{id}/delete", s.requireLogin(s.deleteMessage))

	mux.Handle("GET /media", s.requireLogin(s.mediaPage))
	mux.Handle("POST /media", s.requireLoginStream(s.upload))
	mux.Handle("POST /media/url", s.requireLogin(s.fetchMedia))
	mux.Handle("GET /media/{id}/file", s.requireLogin(s.mediaFile))
	mux.Handle("GET /sounds", s.requireLogin(s.searchSounds))
	mux.Handle("POST /prefs/preview-volume", s.requireLogin(s.savePreviewVolume))
	mux.Handle("POST /media/{id}", s.requireLogin(s.updateMedia))
	mux.Handle("POST /media/{id}/play", s.requireLogin(s.playMedia))
	mux.Handle("POST /media/{id}/delete", s.requireLogin(s.deleteMedia))

	mux.Handle("GET /trash", s.requireLogin(s.trash))
	mux.Handle("POST /trash/{kind}/{id}/restore", s.requireLogin(s.restoreTrash))
	mux.Handle("POST /trash/{kind}/{id}/purge", s.requireAdmin(s.purgeTrash))
	mux.Handle("POST /trash/empty", s.requireAdmin(s.emptyTrash))

	mux.Handle("GET /admin", s.requireAdmin(s.admin))
	mux.Handle("GET /admin/{tab}", s.requireAdmin(s.admin))
	mux.Handle("POST /admin/settings", s.requireAdmin(s.saveSettings))
	mux.Handle("POST /admin/access/{user}", s.requireAdmin(s.decideAccess))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self'; img-src 'self' "+discordCDN+"; media-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		// no-referrer would make browsers send "Origin: null" on form posts
		// https://fetch.spec.whatwg.org/#serializing-a-request-origin
		h.Set("Referrer-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

type ctxKey struct{}

func sessionFrom(r *http.Request) *store.Session {
	sess, _ := r.Context().Value(ctxKey{}).(*store.Session)
	return sess
}

func (s *Server) requireLogin(h http.HandlerFunc) http.Handler { return s.authed(h, false) }

// requireLoginStream is for multipart uploads: the body is not parsed here and
// the handler must check the CSRF token itself with validCSRF.
func (s *Server) requireLoginStream(h http.HandlerFunc) http.Handler { return s.authed(h, true) }

func (s *Server) authed(h http.HandlerFunc, stream bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			s.render(w, r, http.StatusOK, "login", nil)
			return
		}
		sess, err := s.store.GetSession(r.Context(), c.Value, idleTimeout, maxSession)
		if errors.Is(err, store.ErrNotFound) {
			clearCookie(w, sessionCookie)
			s.render(w, r, http.StatusOK, "login", nil)
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		// BOT_ADMINS and BOT_ADMIN_GROUP changes apply to existing sessions after a restart
		sess.IsAdmin = s.isAdmin(sess)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess))
		if sess.Discord() && !discordAllowed(r) {
			s.fail(w, r, http.StatusForbidden, "Discord accounts can only open the start page for now.")
			return
		}
		if r.Method == http.MethodPost && !(stream && s.validOrigin(r, sess) || !stream && s.validPost(w, r, sess)) {
			s.fail(w, r, http.StatusForbidden, "The request could not be verified. Reload the page and try again.")
			return
		}
		h(w, r)
	})
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func (s *Server) requireDiscord(h http.HandlerFunc) http.Handler {
	return s.requireLogin(func(w http.ResponseWriter, r *http.Request) {
		if !sessionFrom(r).Discord() {
			s.notFound(w, r)
			return
		}
		h(w, r)
	})
}

func (s *Server) requireAdmin(h http.HandlerFunc) http.Handler {
	return s.requireLogin(func(w http.ResponseWriter, r *http.Request) {
		if !sessionFrom(r).IsAdmin {
			s.fail(w, r, http.StatusForbidden, "Only admins can open this page. Admins are listed in BOT_ADMINS or are members of a group in BOT_ADMIN_GROUP, both in /app/data/env.sh.")
			return
		}
		h(w, r)
	})
}

// validPost enforces same-origin and the per-session CSRF token on every state change.
func (s *Server) validPost(w http.ResponseWriter, r *http.Request, sess *store.Session) bool {
	if !s.validOrigin(r, sess) {
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		return s.rejectPost(r, sess, "invalid form: "+err.Error())
	}
	return s.validCSRF(r, sess, r.PostForm.Get("csrf"))
}

func (s *Server) validOrigin(r *http.Request, sess *store.Session) bool {
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.cfg.Origin {
		return s.rejectPost(r, sess, "origin mismatch")
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return s.rejectPost(r, sess, "not same-origin")
	}
	return true
}

func (s *Server) validCSRF(r *http.Request, sess *store.Session, token string) bool {
	if subtle.ConstantTimeCompare([]byte(token), []byte(sess.CSRFToken)) != 1 {
		return s.rejectPost(r, sess, "csrf token mismatch")
	}
	return true
}

func (s *Server) rejectPost(r *http.Request, sess *store.Session, reason string) bool {
	s.log.Warn("post rejected", "path", r.URL.Path, "user", sess.Username, "reason", reason,
		"origin", r.Header.Get("Origin"), "sec_fetch_site", r.Header.Get("Sec-Fetch-Site"))
	return false
}

type pageData struct {
	Title     string
	Session   *store.Session
	Prefs     store.Prefs
	Nav       string // highlighted entry of the main navigation
	Path      string // where the account sheet returns to
	Bot       bot.Status
	Error     string
	Notice    string
	Undo      *trashRef // restore button next to the notice after a delete
	Trash     int       // items in the trash
	TrashDays int
	Pending   int // open access requests, for admins
	// DiscordLogin shows the Discord button on the login page
	DiscordLogin bool
	Data         any
}

var navOf = map[string]string{"dashboard": "home", "servers": "servers", "server": "servers", "tasks": "tasks", "task": "tasks",
	"messages": "messages", "message": "messages", "media": "media", "trash": "trash", "admin": "admin", "access": "home"}

// notices are shown after a redirect with ?done=<key>.
var notices = map[string]string{
	"saved":         "Saved.",
	"started":       "Started. The run shows up below in a moment.",
	"paused":        "Paused.",
	"resumed":       "Active again.",
	"deleted":       "Deleted.",
	"sent":          "Sent.",
	"uploaded":      "Uploaded.",
	"playing":       "Playing. It shows up on the dashboard in a moment.",
	"stopped":       "Stopped.",
	"left":          "The bot left the voice channel.",
	"joining":       "Joining. The bot shows up in the channel in a moment.",
	"refreshed":     "Members requested from Discord. Large servers take a few seconds.",
	"copied":        "Copy created.",
	"trashed":       "Moved to the Trash.",
	"restored":      "Restored.",
	"restored-task": "Restored. It stays paused until you resume it.",
	"purged":        "Deleted for good.",
	"emptied":       "The trash is empty.",

	"requested":        "Request sent.",
	"withdrawn":        "Request withdrawn.",
	"access-approved":  "Approved. They can log in now.",
	"access-denied":    "Request denied.",
	"access-blocked":   "Blocked.",
	"access-unblocked": "Unblocked. They can ask for access again.",
	"access-revoked":   "Access revoked.",
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	s.renderMsg(w, r, status, page, "", data)
}

func (s *Server) renderMsg(w http.ResponseWriter, r *http.Request, status int, page, msg string, data any) {
	sess := sessionFrom(r)
	prefs := store.DefaultPrefs
	trash, trashDays, pending := 0, store.DefaultSettings.TrashRetentionDays, 0
	if sess != nil && s.store != nil {
		if p, err := s.store.Prefs(r.Context(), sess.Username); err != nil {
			s.log.Error("reading preferences", "user", sess.Username, "err", err)
		} else {
			prefs = p
		}
		var err error
		if !sess.Discord() {
			if trash, err = s.store.CountTrash(r.Context()); err != nil {
				s.log.Error("counting the trash", "err", err)
			}
		}
		if sess.IsAdmin {
			if pending, err = s.store.PendingAccessCount(r.Context()); err != nil {
				s.log.Error("counting access requests", "err", err)
			}
		}
		if st, err := s.store.Settings(r.Context()); err == nil {
			trashDays = st.TrashRetentionDays
		}
	}
	q := r.URL.Query()
	notice := notices[q.Get("done")]
	var undo *trashRef
	switch q.Get("done") {
	case "trashed":
		if sess != nil {
			undo = parseTrashRef(q.Get("undo"))
		}
	case "renamed":
		if as := q.Get("as"); as != "" && len(as) <= 300 {
			notice = "Restored as " + as + ", the old name is taken."
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	err := s.pages[page].Execute(w, pageData{
		Title:        "PTS Discord Bot",
		Session:      sess,
		Prefs:        prefs,
		Nav:          navOf[page],
		Path:         r.URL.Path,
		Bot:          s.bot.Status(),
		Error:        msg,
		Notice:       notice,
		Undo:         undo,
		Trash:        trash,
		TrashDays:    trashDays,
		Pending:      pending,
		DiscordLogin: s.discordEnabled(),
		Data:         data,
	})
	if err != nil {
		s.log.Error("render", "page", page, "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.renderMsg(w, r, status, "error", msg, nil)
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "err", err)
	s.fail(w, r, http.StatusInternalServerError, "Internal error, see the app logs.")
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusNotFound, "Not found. It may have been deleted.")
}

// storeErr shows not found errors, everything else is logged.
func (s *Server) storeErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	s.internalError(w, r, err)
}

func (s *Server) audit(r *http.Request, action, target, detail string) {
	s.auditAs(r.Context(), sessionFrom(r).Username, action, target, detail)
}

func (s *Server) auditAs(ctx context.Context, actor, action, target, detail string) {
	s.log.Info("audit", "actor", actor, "action", action, "target", target, "detail", detail)
	if err := s.store.Audit(ctx, store.AuditEntry{Actor: actor, Action: action, Target: target, Detail: detail}); err != nil {
		s.log.Error("audit write failed", "err", err)
	}
}

func redirect(w http.ResponseWriter, r *http.Request, path, done string) {
	if done != "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		path += sep + "done=" + done
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok\n"))
}

// PurgeLoop removes expired sessions, old runs and items that were long enough
// in the trash, and keeps Discord access in line with server membership.
func (s *Server) PurgeLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		st, err := s.store.Settings(ctx)
		if err != nil {
			st = store.DefaultSettings
		}
		if err := s.store.Purge(ctx, idleTimeout, maxSession, time.Duration(st.RunRetentionDays)*24*time.Hour); err != nil && ctx.Err() == nil {
			s.log.Error("purge", "err", err)
		}
		n, ids, err := s.store.PurgeTrash(ctx, time.Duration(st.TrashRetentionDays)*24*time.Hour)
		if err != nil && ctx.Err() == nil {
			s.log.Error("purging the trash", "err", err)
		}
		for _, id := range ids {
			s.removeMediaFile(id)
		}
		if n > 0 {
			s.log.Info("removed items from the trash", "count", n)
		}
		err = maintainAccess(ctx, s.store, s.bot, func(action, target, detail string) { s.auditAs(ctx, store.SystemActor, action, target, detail) })
		if err != nil && ctx.Err() == nil {
			s.log.Error("checking discord access", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) oauthConfig(ctx context.Context) (*oidc.Provider, *oauth2.Config, error) {
	s.oidcMu.Lock()
	defer s.oidcMu.Unlock()
	if s.provider == nil {
		p, err := oidc.NewProvider(ctx, s.cfg.Issuer)
		if err != nil {
			return nil, nil, err
		}
		s.provider = p
	}
	return s.provider, &oauth2.Config{
		ClientID:     s.cfg.ClientID,
		ClientSecret: s.cfg.ClientSecret,
		Endpoint:     s.provider.Endpoint(),
		RedirectURL:  s.cfg.Origin + "/auth/callback",
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email", "groups"},
	}, nil
}

// safeNext only allows paths on this site as redirect target.
func safeNext(next, fallback string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return fallback
	}
	return next
}

func (s *Server) saveAccount(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	p := store.Prefs{ShowLabels: r.PostForm.Get("show_labels") == "on", ConfirmActions: r.PostForm.Get("confirm_actions") == "on"}
	if err := s.store.SetPrefs(r.Context(), sess.Username, p); err != nil {
		s.internalError(w, r, err)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, safeNext(r.PostForm.Get("next"), "/"), http.StatusSeeOther)
}
