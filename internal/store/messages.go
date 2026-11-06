package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
)

const messageCols = `id, name, content, embeds, files, updated_by, updated_at`

func scanMessage(r pgx.Row) (messages.Message, error) {
	var m messages.Message
	err := r.Scan(&m.ID, &m.Name, &m.Content, &m.Embeds, &m.Files, &m.UpdatedBy, &m.UpdatedAt)
	if m.Embeds == nil {
		m.Embeds = []messages.Embed{}
	}
	return m, err
}

func (s *Store) Message(ctx context.Context, id int64) (messages.Message, error) {
	m, err := scanMessage(s.pool.QueryRow(ctx, `SELECT `+messageCols+` FROM messages WHERE id = $1 AND deleted_at IS NULL`, id))
	return m, noRows(err)
}

func (s *Store) ListMessages(ctx context.Context) ([]messages.Message, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+messageCols+` FROM messages WHERE deleted_at IS NULL ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (messages.Message, error) { return scanMessage(r) })
}

func (s *Store) CreateMessage(ctx context.Context, m messages.Message, by string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO messages (name, content, embeds, files, created_by, updated_by) VALUES ($1, $2, $3, $4, $5, $5) RETURNING id`,
		m.Name, m.Content, m.Embeds, files(m.Files), by).Scan(&id)
	return id, err
}

func (s *Store) UpdateMessage(ctx context.Context, m messages.Message, by string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE messages SET name = $2, content = $3, embeds = $4, files = $5, updated_by = $6, updated_at = now() WHERE id = $1 AND deleted_at IS NULL`,
		m.ID, m.Name, m.Content, m.Embeds, files(m.Files), by)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteMessage(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM messages WHERE id = $1`, id)
	return err
}

// MessageUsers returns the names of the tasks that send the message.
func (s *Store) MessageUsers(ctx context.Context, id int64) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT name FROM tasks WHERE deleted_at IS NULL AND steps @> jsonb_build_array(jsonb_build_object('message_id', $1::bigint)) ORDER BY lower(name)`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func files(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}
