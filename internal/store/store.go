// Package store keeps the panel's state in the Cloudron PostgreSQL addon.
package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, connString string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// CreateSchema runs as one implicit transaction, either all tables exist or none.
func (s *Store) CreateSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, schema)
	return err
}

func hash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func noRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Login providers of a session.
const (
	ProviderCloudron = "cloudron"
	ProviderDiscord  = "discord"
)

type Session struct {
	Username    string // Cloudron user ID, or discord:<user ID>
	DisplayName string
	IsAdmin     bool     // computed per request from Groups and the admin config
	Groups      []string // Cloudron groups at login
	CSRFToken   string
	Provider    string
	GuildIDs    []string // Discord: servers shared with the bot at login
	Granted     []string // Discord: approved servers of GuildIDs, set per request
}

func (s *Session) Discord() bool { return s.Provider == ProviderDiscord }

// NoAccess is a Discord user without an approved server, who only sees the access page.
func (s *Session) NoAccess() bool { return s.Discord() && len(s.Granted) == 0 }

// Scope is the tasks and runs the user may see: everything for Cloudron users, the approved servers for Discord users.
func (s *Session) Scope() Scope {
	if s.Discord() {
		return GuildScope(s.Granted...)
	}
	return All
}

func (s *Session) CanGuild(guildID string) bool {
	return !s.Discord() || guildID != "" && s.Scope().Has(guildID)
}

// CreateSession stores only a hash of the token, a database leak does not expose live sessions.
func (s *Store) CreateSession(ctx context.Context, token string, sess Session) error {
	provider := sess.Provider
	if provider == "" {
		provider = ProviderCloudron
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO sessions (id_hash, username, display_name, is_admin, groups, csrf_token, provider, guild_ids) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		hash(token), sess.Username, sess.DisplayName, sess.IsAdmin, groupsOrEmpty(sess.Groups), sess.CSRFToken, provider, groupsOrEmpty(sess.GuildIDs))
	return err
}

func (s *Store) GetSession(ctx context.Context, token string, idle, absolute time.Duration) (*Session, error) {
	var sess Session
	err := s.pool.QueryRow(ctx, `
UPDATE sessions SET last_seen_at = now()
WHERE id_hash = $1 AND last_seen_at > now() - $2 * interval '1 second' AND created_at > now() - $3 * interval '1 second'
RETURNING username, display_name, is_admin, groups, csrf_token, provider, guild_ids`,
		hash(token), int(idle.Seconds()), int(absolute.Seconds())).Scan(&sess.Username, &sess.DisplayName, &sess.IsAdmin, &sess.Groups, &sess.CSRFToken, &sess.Provider, &sess.GuildIDs)
	if err != nil {
		return nil, noRows(err)
	}
	return &sess, nil
}

func groupsOrEmpty(g []string) []string {
	if g == nil {
		return []string{}
	}
	return g
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id_hash = $1`, hash(token))
	return err
}

// Purge removes expired sessions and logins and runs older than the retention.
func (s *Store) Purge(ctx context.Context, idle, absolute time.Duration, runRetention time.Duration) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE last_seen_at < now() - $1 * interval '1 second' OR created_at < now() - $2 * interval '1 second'`,
		int(idle.Seconds()), int(absolute.Seconds())); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM oidc_logins WHERE created_at < now() - interval '10 minutes'`); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM runs WHERE status <> 'running' AND started_at < now() - $1 * interval '1 second'`, int(runRetention.Seconds()))
	return err
}

func (s *Store) SaveOIDCLogin(ctx context.Context, provider, state, verifier, nonce string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO oidc_logins (state_hash, verifier, nonce, provider) VALUES ($1, $2, $3, $4)`, hash(state), verifier, nonce, provider)
	return err
}

// TakeOIDCLogin returns and deletes a pending login, so each state can be used
// once and only with the provider it was started for.
func (s *Store) TakeOIDCLogin(ctx context.Context, provider, state string) (verifier, nonce string, err error) {
	err = s.pool.QueryRow(ctx, `DELETE FROM oidc_logins WHERE state_hash = $1 AND provider = $2 AND created_at > now() - interval '10 minutes' RETURNING verifier, nonce`,
		hash(state), provider).Scan(&verifier, &nonce)
	return verifier, nonce, noRows(err)
}

type AuditEntry struct {
	At     time.Time
	Actor  string
	Action string
	Target string
	Detail string
}

func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_log (actor, action, target, detail) VALUES ($1, $2, $3, $4)`, e.Actor, e.Action, e.Target, e.Detail)
	return err
}

// AuditQuery filters the audit log. Category is the part of the action
// before the dot, Search matches any column as a substring.
type AuditQuery struct {
	Category, Search string
	Limit, Offset    int
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (s *Store) ListAudit(ctx context.Context, q AuditQuery) ([]AuditEntry, error) {
	rows, err := s.pool.Query(ctx, `SELECT at, actor, action, target, detail FROM audit_log
		WHERE ($1 = '' OR split_part(action, '.', 1) = $1)
		  AND ($2 = '' OR concat_ws(' ', actor, action, target, detail) ILIKE '%' || $2 || '%')
		ORDER BY id DESC LIMIT $3 OFFSET $4`, q.Category, likeEscaper.Replace(q.Search), q.Limit, q.Offset)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AuditEntry, error) {
		var e AuditEntry
		err := r.Scan(&e.At, &e.Actor, &e.Action, &e.Target, &e.Detail)
		return e, err
	})
}

type Prefs struct {
	ShowLabels     bool // text next to every icon button
	ConfirmActions bool // ask once before moving to the Trash or restoring
	PreviewVolume  int  // percent, for every sound preview in the browser
}

var DefaultPrefs = Prefs{ConfirmActions: true, PreviewVolume: 100}

func (s *Store) Prefs(ctx context.Context, username string) (Prefs, error) {
	var p Prefs
	err := s.pool.QueryRow(ctx, `SELECT show_labels, confirm_actions, preview_volume FROM user_prefs WHERE username = $1`, username).Scan(&p.ShowLabels, &p.ConfirmActions, &p.PreviewVolume)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultPrefs, nil
	}
	return p, err
}

func (s *Store) SetPrefs(ctx context.Context, username string, p Prefs) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO user_prefs (username, show_labels, confirm_actions) VALUES ($1, $2, $3)
ON CONFLICT (username) DO UPDATE SET show_labels = EXCLUDED.show_labels, confirm_actions = EXCLUDED.confirm_actions`, username, p.ShowLabels, p.ConfirmActions)
	return err
}

func (s *Store) SetPreviewVolume(ctx context.Context, username string, volume int) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO user_prefs (username, preview_volume) VALUES ($1, $2)
ON CONFLICT (username) DO UPDATE SET preview_volume = EXCLUDED.preview_volume`, username, volume)
	return err
}

// Settings are changed on the admin page.
type Settings struct {
	Timezone           string // default for new tasks
	MaxPlayMinutes     int
	MaxUploadMB        int
	FailureChannelID   string // empty: no failure posts
	RunRetentionDays   int
	TrashRetentionDays int // 0: deleting removes right away
}

var DefaultSettings = Settings{Timezone: "Europe/Berlin", MaxPlayMinutes: 10, MaxUploadMB: 100, RunRetentionDays: 90, TrashRetentionDays: 30}

func (s *Store) Settings(ctx context.Context) (Settings, error) {
	out := DefaultSettings
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return out, err
	}
	kv, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([2]string, error) {
		var p [2]string
		err := r.Scan(&p[0], &p[1])
		return p, err
	})
	if err != nil {
		return out, err
	}
	for _, p := range kv {
		n, nerr := strconv.Atoi(p[1])
		switch p[0] {
		case "timezone":
			out.Timezone = p[1]
		case "max_play_minutes":
			if n > 0 {
				out.MaxPlayMinutes = n
			}
		case "max_upload_mb":
			if n > 0 {
				out.MaxUploadMB = n
			}
		case "failure_channel_id":
			out.FailureChannelID = p[1]
		case "run_retention_days":
			if n > 0 {
				out.RunRetentionDays = n
			}
		case "trash_retention_days":
			if nerr == nil && n >= 0 {
				out.TrashRetentionDays = n
			}
		}
	}
	return out, nil
}

func (s *Store) SaveSettings(ctx context.Context, st Settings) error {
	kv := map[string]string{
		"timezone":             st.Timezone,
		"max_play_minutes":     strconv.Itoa(st.MaxPlayMinutes),
		"max_upload_mb":        strconv.Itoa(st.MaxUploadMB),
		"failure_channel_id":   st.FailureChannelID,
		"run_retention_days":   strconv.Itoa(st.RunRetentionDays),
		"trash_retention_days": strconv.Itoa(st.TrashRetentionDays),
	}
	batch := &pgx.Batch{}
	for k, v := range kv {
		batch.Queue(`INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, k, v)
	}
	return s.pool.SendBatch(ctx, batch).Close()
}
