package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
)

const messageCols = `id, name, content, embeds, files, updated_by, updated_at, coalesce(guild_id, ''), group_only`

func messageFields(m *messages.Message) []any {
	return []any{&m.ID, &m.Name, &m.Content, &m.Embeds, &m.Files, &m.UpdatedBy, &m.UpdatedAt, &m.GuildID, &m.GroupOnly}
}

func scanMessage(r pgx.Row) (messages.Message, error) {
	var m messages.Message
	err := r.Scan(messageFields(&m)...)
	if m.Embeds == nil {
		m.Embeds = []messages.Embed{}
	}
	return m, err
}

func (s *Store) Message(ctx context.Context, id int64) (messages.Message, error) {
	m, err := scanMessage(s.pool.QueryRow(ctx, `SELECT `+messageCols+` FROM messages WHERE id = $1 AND deleted_at IS NULL`, id))
	return m, noRows(err)
}

// ListMessages returns the messages offered to a server, the server's own first, all of them when guildID is empty.
func (s *Store) ListMessages(ctx context.Context, guildID string) ([]messages.Message, error) {
	return s.FindMessages(ctx, MessageQuery{Offer: guildID})
}

// MessageQuery filters the messages. Search matches names and text, a zero
// Limit lists all. Offer and Group work as in MediaQuery.
type MessageQuery struct {
	Offer, Group, Search string
	Limit, Offset        int
}

func (s *Store) FindMessages(ctx context.Context, q MessageQuery) ([]messages.Message, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+messageCols+` FROM messages g WHERE deleted_at IS NULL AND `+offered("g", "4")+` AND `+inGroup("g", "5")+`
  AND ($1 = '' OR name ILIKE '%' || $1 || '%' OR content ILIKE '%' || $1 || '%')
ORDER BY `+ownFirst("g", "4")+`, lower(name), id LIMIT NULLIF($2, 0) OFFSET $3`, likeEscaper.Replace(q.Search), q.Limit, q.Offset, q.Offer, q.Group)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (messages.Message, error) { return scanMessage(r) })
}

func (s *Store) CreateMessage(ctx context.Context, m messages.Message, by string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO messages (name, content, embeds, files, created_by, updated_by, guild_id, group_only) VALUES ($1, $2, $3, $4, $5, $5, $6, $7) RETURNING id`,
		m.Name, m.Content, m.Embeds, files(m.Files), by, nullable(m.GuildID), m.GroupOnly && m.GuildID != "").Scan(&id)
	return id, err
}

func (s *Store) UpdateMessage(ctx context.Context, m messages.Message, by string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE messages SET name = $2, content = $3, embeds = $4, files = $5, updated_by = $6, updated_at = now(), guild_id = $7, group_only = $8
WHERE id = $1 AND deleted_at IS NULL`, m.ID, m.Name, m.Content, m.Embeds, files(m.Files), by, nullable(m.GuildID), m.GroupOnly && m.GuildID != "")
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteMessage(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM messages WHERE id = $1`, id)
	return err
}

// MessageUsers returns the names of the tasks in scope that send the message and counts the others.
func (s *Store) MessageUsers(ctx context.Context, sc Scope, id int64) (names []string, others int, err error) {
	rows, err := s.pool.Query(ctx, `SELECT name, `+inScope("guild_id", "2")+` FROM tasks
WHERE deleted_at IS NULL AND steps @> jsonb_build_array(jsonb_build_object('message_id', $1::bigint)) ORDER BY lower(name)`, id, sc.param())
	if err != nil {
		return nil, 0, err
	}
	var name string
	var visible bool
	_, err = pgx.ForEachRow(rows, []any{&name, &visible}, func() error {
		if visible {
			names = append(names, name)
		} else {
			others++
		}
		return nil
	})
	return names, others, err
}

func files(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}
