package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// States of a Discord user on the admin Access tab. Requests are pending,
// approved, denied or revoked; blocked is a flag of the user.
const (
	AccessPending  = "pending"
	AccessApproved = "approved"
	AccessDenied   = "denied"
	AccessRevoked  = "revoked"
	AccessBlocked  = "blocked"
)

// SystemActor decides requests nobody answered in time.
const SystemActor = "system"

var (
	ErrPending = errors.New("a request is already open")
	ErrBlocked = errors.New("the user is blocked")
)

type DiscordUser struct {
	ID         string
	Username   string
	GlobalName string
	Avatar     string // hash for cdn.discordapp.com, empty for the default avatar
	Blocked    bool
	BlockedBy  string
	BlockedAt  *time.Time
}

func (u DiscordUser) Name() string {
	if u.GlobalName != "" {
		return u.GlobalName
	}
	return u.Username
}

// DiscordSubject is the session username of a Discord user. The colon cannot
// appear in Cloudron usernames, so it never matches BOT_ADMINS.
func DiscordSubject(userID string) string { return "discord:" + userID }

type AccessRequest struct {
	ID          int64
	UserID      string
	GuildIDs    []string
	Note        string
	Status      string
	RequestedAt time.Time
	ExpiresAt   time.Time
	DecidedBy   string
	DecidedAt   *time.Time
}

type Grant struct {
	UserID  string
	GuildID string
}

// UpsertDiscordUser records a login and reports whether the user is blocked.
func (s *Store) UpsertDiscordUser(ctx context.Context, u DiscordUser) (blocked bool, err error) {
	err = s.pool.QueryRow(ctx, `INSERT INTO discord_users (user_id, username, global_name, avatar) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO UPDATE SET username = EXCLUDED.username, global_name = EXCLUDED.global_name, avatar = EXCLUDED.avatar, last_login_at = now()
RETURNING blocked`, u.ID, u.Username, u.GlobalName, u.Avatar).Scan(&blocked)
	return blocked, err
}

// DiscordAccess is what a Discord user may do right now.
type DiscordAccess struct {
	User   DiscordUser
	Grants []string
	Latest *AccessRequest
}

const requestCols = `id, user_id, guild_ids, note, status, requested_at, expires_at, coalesce(decided_by, '') AS decided_by, decided_at`

func scanRequest(r pgx.Row) (AccessRequest, error) {
	var a AccessRequest
	err := r.Scan(&a.ID, &a.UserID, &a.GuildIDs, &a.Note, &a.Status, &a.RequestedAt, &a.ExpiresAt, &a.DecidedBy, &a.DecidedAt)
	return a, err
}

func (s *Store) DiscordAccess(ctx context.Context, userID string) (DiscordAccess, error) {
	var a DiscordAccess
	u := &a.User
	err := s.pool.QueryRow(ctx, `SELECT user_id, username, global_name, avatar, blocked FROM discord_users WHERE user_id = $1`, userID).
		Scan(&u.ID, &u.Username, &u.GlobalName, &u.Avatar, &u.Blocked)
	if err != nil {
		return a, noRows(err)
	}
	rows, err := s.pool.Query(ctx, `SELECT guild_id FROM discord_grants WHERE user_id = $1 ORDER BY guild_id`, userID)
	if err != nil {
		return a, err
	}
	if a.Grants, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return a, err
	}
	req, err := scanRequest(s.pool.QueryRow(ctx, `SELECT `+requestCols+` FROM access_requests WHERE user_id = $1 ORDER BY id DESC LIMIT 1`, userID))
	if err == nil {
		a.Latest = &req
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return a, err
	}
	return a, nil
}

// GrantedGuilds are the servers a Discord user was approved for.
func (s *Store) GrantedGuilds(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT guild_id FROM discord_grants WHERE user_id = $1 ORDER BY guild_id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// CreateAccessRequest fails with ErrBlocked for blocked users and ErrPending
// when the user already waits for an answer.
func (s *Store) CreateAccessRequest(ctx context.Context, userID string, guildIDs []string, note string) (AccessRequest, error) {
	req, err := scanRequest(s.pool.QueryRow(ctx, `INSERT INTO access_requests (user_id, guild_ids, note)
SELECT $1, $2, $3 WHERE NOT EXISTS (SELECT 1 FROM discord_users WHERE user_id = $1 AND blocked)
RETURNING `+requestCols, userID, guildIDs, note))
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return req, ErrBlocked
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		return req, ErrPending
	}
	return req, err
}

func (s *Store) WithdrawAccessRequest(ctx context.Context, userID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM access_requests WHERE user_id = $1 AND status = 'pending'`, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func affected(tag pgconn.CommandTag, err error) error {
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func dropDiscordSessions(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE provider = 'discord' AND username = $1`, DiscordSubject(userID))
	return err
}

// ApproveAccess grants the given servers, which must be part of the open request.
func (s *Store) ApproveAccess(ctx context.Context, userID string, guildIDs []string, by string) error {
	if len(guildIDs) == 0 {
		return ErrNotFound
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := affected(tx.Exec(ctx, `UPDATE access_requests SET status = 'approved', decided_by = $3, decided_at = now()
WHERE user_id = $1 AND status = 'pending' AND guild_ids @> $2`, userID, guildIDs, by)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO discord_grants (user_id, guild_id, granted_by) SELECT $1, unnest($2::text[]), $3 ON CONFLICT DO NOTHING`, userID, guildIDs, by)
		return err
	})
}

func (s *Store) DenyAccess(ctx context.Context, userID, by string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := affected(tx.Exec(ctx, `UPDATE access_requests SET status = 'denied', decided_by = $2, decided_at = now() WHERE user_id = $1 AND status = 'pending'`, userID, by)); err != nil {
			return err
		}
		return dropDiscordSessions(ctx, tx, userID)
	})
}

func (s *Store) RevokeAccess(ctx context.Context, userID, by string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := affected(tx.Exec(ctx, `DELETE FROM discord_grants WHERE user_id = $1`, userID)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE access_requests SET status = 'revoked', decided_by = $2, decided_at = now()
WHERE id = (SELECT max(id) FROM access_requests WHERE user_id = $1 AND status = 'approved')`, userID, by); err != nil {
			return err
		}
		return dropDiscordSessions(ctx, tx, userID)
	})
}

// BlockDiscordUser removes all access, denies an open request and keeps the user from asking again.
func (s *Store) BlockDiscordUser(ctx context.Context, userID, by string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := affected(tx.Exec(ctx, `UPDATE discord_users SET blocked = true, blocked_by = $2, blocked_at = now() WHERE user_id = $1 AND NOT blocked`, userID, by)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM discord_grants WHERE user_id = $1`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE access_requests SET status = 'denied', decided_by = $2, decided_at = now() WHERE user_id = $1 AND status = 'pending'`, userID, by); err != nil {
			return err
		}
		return dropDiscordSessions(ctx, tx, userID)
	})
}

func (s *Store) UnblockDiscordUser(ctx context.Context, userID string) error {
	return affected(s.pool.Exec(ctx, `UPDATE discord_users SET blocked = false, blocked_by = NULL, blocked_at = NULL WHERE user_id = $1 AND blocked`, userID))
}

// AutoDenyExpired denies pending requests past their expiry and returns them.
func (s *Store) AutoDenyExpired(ctx context.Context) ([]AccessRequest, error) {
	rows, err := s.pool.Query(ctx, `UPDATE access_requests SET status = 'denied', decided_by = $1, decided_at = now()
WHERE status = 'pending' AND expires_at <= now() RETURNING `+requestCols, SystemActor)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AccessRequest, error) { return scanRequest(r) })
}

func (s *Store) AllGrants(ctx context.Context) ([]Grant, error) {
	rows, err := s.pool.Query(ctx, `SELECT user_id, guild_id FROM discord_grants ORDER BY user_id, guild_id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Grant])
}

// RemoveGrant ends access to one server and logs the user out when no access is left.
func (s *Store) RemoveGrant(ctx context.Context, userID, guildID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := affected(tx.Exec(ctx, `DELETE FROM discord_grants WHERE user_id = $1 AND guild_id = $2`, userID, guildID)); err != nil {
			return err
		}
		var left bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM discord_grants WHERE user_id = $1)`, userID).Scan(&left); err != nil || left {
			return err
		}
		return dropDiscordSessions(ctx, tx, userID)
	})
}

// RemoveGuildGrants ends all access to a server the bot left and returns the users who lost it.
func (s *Store) RemoveGuildGrants(ctx context.Context, guildID string) ([]string, error) {
	var users []string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `DELETE FROM discord_grants WHERE guild_id = $1 RETURNING user_id`, guildID)
		if err != nil {
			return err
		}
		if users, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		for _, u := range users {
			var left bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM discord_grants WHERE user_id = $1)`, u).Scan(&left); err != nil {
				return err
			}
			if !left {
				if err := dropDiscordSessions(ctx, tx, u); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return users, err
}

func (s *Store) PendingAccessCount(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM access_requests WHERE status = 'pending'`).Scan(&n)
	return n, err
}

// AccessEntry is one Discord user on the Access tab.
type AccessEntry struct {
	User      DiscordUser
	State     string // pending, approved, denied or blocked
	Request   *AccessRequest
	Grants    []string
	GrantedBy string
	GrantedAt *time.Time
}

// accessEntries gives every Discord user who asked for access, got it or was
// blocked one state, in this order: blocked, pending, approved, denied.
const accessEntries = `WITH latest AS (
    SELECT DISTINCT ON (user_id) ` + requestCols + ` FROM access_requests ORDER BY user_id, id DESC
), grants AS (
    SELECT user_id, array_agg(guild_id ORDER BY guild_id) AS guild_ids,
        (array_agg(granted_by ORDER BY granted_at DESC))[1] AS granted_by, max(granted_at) AS granted_at
    FROM discord_grants GROUP BY user_id
), entries AS (
    SELECT u.user_id, u.username, u.global_name, u.avatar, u.blocked, coalesce(u.blocked_by, '') AS blocked_by, u.blocked_at,
        l.id AS req_id, l.guild_ids AS req_guilds, l.note, l.status, l.requested_at, l.expires_at, l.decided_by, l.decided_at,
        g.guild_ids AS grant_guilds, coalesce(g.granted_by, '') AS granted_by, g.granted_at,
        CASE WHEN u.blocked THEN 'blocked' WHEN l.status = 'pending' THEN 'pending'
            WHEN g.user_id IS NOT NULL THEN 'approved' WHEN l.id IS NOT NULL THEN 'denied' END AS state
    FROM discord_users u LEFT JOIN latest l ON l.user_id = u.user_id LEFT JOIN grants g ON g.user_id = u.user_id
)
`

func (s *Store) AccessCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, accessEntries+`SELECT state, count(*) FROM entries WHERE state IS NOT NULL GROUP BY state`)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	var state string
	var n int
	_, err = pgx.ForEachRow(rows, []any{&state, &n}, func() error {
		out[state] = n
		return nil
	})
	return out, err
}

// AccessQuery selects one state. Search matches names and usernames as a
// substring, GuildIDs are servers whose name matched the search.
type AccessQuery struct {
	State         string
	Search        string
	GuildIDs      []string
	Limit, Offset int
}

func (s *Store) ListAccess(ctx context.Context, q AccessQuery) ([]AccessEntry, int, error) {
	if q.GuildIDs == nil {
		q.GuildIDs = []string{}
	}
	rows, err := s.pool.Query(ctx, accessEntries+`SELECT user_id, username, global_name, avatar, blocked, blocked_by, blocked_at,
    req_id, req_guilds, note, status, requested_at, expires_at, coalesce(decided_by, ''), decided_at,
    grant_guilds, granted_by, granted_at, state, count(*) OVER ()
FROM entries
WHERE state = $1
  AND ($2 = '' OR concat_ws(' ', username, global_name) ILIKE '%' || $2 || '%'
       OR coalesce(req_guilds, '{}') && $3 OR coalesce(grant_guilds, '{}') && $3)
ORDER BY CASE WHEN state = 'pending' THEN expires_at END ASC,
    coalesce(blocked_at, granted_at, decided_at, requested_at) DESC, user_id
LIMIT $4 OFFSET $5`, q.State, likeEscaper.Replace(q.Search), q.GuildIDs, q.Limit, q.Offset)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AccessEntry, error) {
		var e AccessEntry
		u := &e.User
		var reqID *int64
		var reqGuilds []string
		var note, status *string
		var requestedAt, expiresAt *time.Time
		var decidedBy string
		var decidedAt *time.Time
		err := r.Scan(&u.ID, &u.Username, &u.GlobalName, &u.Avatar, &u.Blocked, &u.BlockedBy, &u.BlockedAt,
			&reqID, &reqGuilds, &note, &status, &requestedAt, &expiresAt, &decidedBy, &decidedAt,
			&e.Grants, &e.GrantedBy, &e.GrantedAt, &e.State, &total)
		if err == nil && reqID != nil {
			e.Request = &AccessRequest{ID: *reqID, UserID: u.ID, GuildIDs: reqGuilds, Note: *note, Status: *status,
				RequestedAt: *requestedAt, ExpiresAt: *expiresAt, DecidedBy: decidedBy, DecidedAt: decidedAt}
		}
		return e, err
	})
	return list, total, err
}
