package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Use is a task or message that references a file or message.
type Use struct {
	Kind    string // "task" or "message"
	Name    string
	GuildID string // "" for a shared message
	run     Scope
}

// Breaks tells whether the user fails to run once the item belongs to guildID, "" being shared.
func (u Use) Breaks(guildID string) bool { return !u.run.Has(guildID) }

// mediaUsers lists the tasks and messages that reference the files of the
// relation from, aliased m. Tag steps count for sounds the task may pick.
func mediaUsers(from string) string {
	return `SELECT m.id AS media_id, 'task' AS kind, t.name, t.guild_id AS owner, t.updated_by, t.created_by FROM ` + from + ` m JOIN tasks t ON t.deleted_at IS NULL
    AND (t.steps @> jsonb_build_array(jsonb_build_object('media_id', m.id))
      OR t.steps @> jsonb_build_array(jsonb_build_object('media_ids', jsonb_build_array(m.id)))
      OR t.steps @> jsonb_build_array(jsonb_build_object('avatar_media_id', m.id))
      OR EXISTS (SELECT FROM jsonb_array_elements(t.steps) st WHERE st->>'tag' = ANY (m.tags) AND coalesce(m.guild_id, t.guild_id) = t.guild_id))
  WHERE m.deleted_at IS NULL
  UNION ALL
  SELECT m.id, 'message', g.name, coalesce(g.guild_id, ''), g.updated_by, '' FROM ` + from + ` m JOIN messages g ON g.deleted_at IS NULL
    AND (m.id = ANY (g.files)
      OR g.embeds @> jsonb_build_array(jsonb_build_object('image_media_id', m.id))
      OR g.embeds @> jsonb_build_array(jsonb_build_object('thumb_media_id', m.id)))
  WHERE m.deleted_at IS NULL`
}

func collectUses(rows pgx.Rows) (map[int64][]Use, error) {
	out := map[int64][]Use{}
	var id int64
	var u Use
	var updated, created string
	_, err := pgx.ForEachRow(rows, []any{&id, &u.Kind, &u.Name, &u.GuildID, &updated, &created}, func() error {
		u.run = RunScope(u.GuildID, updated, created)
		out[id] = append(out[id], u)
		return nil
	})
	return out, err
}

// MediaUses returns per file every task and message that references it, in all servers.
func (s *Store) MediaUses(ctx context.Context, ids []int64) (map[int64][]Use, error) {
	if len(ids) == 0 {
		return map[int64][]Use{}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT * FROM (`+mediaUsers(`(SELECT * FROM media WHERE id = ANY ($1))`)+`) u ORDER BY kind DESC, lower(name)`, ids)
	if err != nil {
		return nil, err
	}
	return collectUses(rows)
}

// MessageUses returns every task that sends the message, in all servers.
func (s *Store) MessageUses(ctx context.Context, id int64) ([]Use, error) {
	rows, err := s.pool.Query(ctx, `SELECT $1::bigint, 'task', name, guild_id, updated_by, created_by FROM tasks
WHERE deleted_at IS NULL AND steps @> jsonb_build_array(jsonb_build_object('message_id', $1::bigint)) ORDER BY lower(name)`, id)
	if err != nil {
		return nil, err
	}
	out, err := collectUses(rows)
	return out[id], err
}

// MediaByIDs returns the live files of ids in any server.
func (s *Store) MediaByIDs(ctx context.Context, ids []int64) ([]Media, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+mediaCols+` FROM media WHERE id = ANY ($1) AND deleted_at IS NULL ORDER BY lower(name)`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Media, error) { return scanMedia(r) })
}

// NameTakenError names the file whose name the new owner has already.
type NameTakenError struct{ Name string }

func (e *NameTakenError) Error() string        { return e.Name + ": name taken" }
func (e *NameTakenError) Is(target error) bool { return target == ErrNameTaken }

// MoveItems gives files and messages to guildID, "" making them shared. It
// moves all or nothing: a file name the new owner has already stops it.
func (s *Store) MoveItems(ctx context.Context, guildID string, mediaIDs, messageIDs []int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, id := range mediaIDs {
		var name string
		if err := tx.QueryRow(ctx, `SELECT name FROM media WHERE id = $1 AND deleted_at IS NULL`, id).Scan(&name); err != nil {
			return noRows(err)
		}
		_, err := tx.Exec(ctx, `UPDATE media SET guild_id = $2 WHERE id = $1`, id, nullable(guildID))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return &NameTakenError{Name: name}
		}
		if err != nil {
			return err
		}
	}
	if len(messageIDs) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE messages SET guild_id = $2 WHERE id = ANY ($1) AND deleted_at IS NULL`, messageIDs, nullable(guildID)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// KnownGuilds are the servers that own tasks, items or Discord grants, also
// those the bot is not connected to right now.
func (s *Store) KnownGuilds(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT guild_id FROM tasks WHERE deleted_at IS NULL AND guild_id <> ''
UNION SELECT guild_id FROM discord_grants
UNION SELECT guild_id FROM media WHERE guild_id IS NOT NULL AND deleted_at IS NULL
UNION SELECT guild_id FROM messages WHERE guild_id IS NOT NULL AND deleted_at IS NULL ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
