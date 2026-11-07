package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// mediaUsers lists the tasks and messages that reference the files of the
// relation from, aliased m. Tag steps count for the sounds offered to the task's server.
func mediaUsers(from string) string {
	return `SELECT m.id AS media_id, 'task' AS kind, t.name, t.guild_id AS owner FROM ` + from + ` m JOIN tasks t ON t.deleted_at IS NULL
    AND (t.steps @> jsonb_build_array(jsonb_build_object('media_id', m.id))
      OR t.steps @> jsonb_build_array(jsonb_build_object('media_ids', jsonb_build_array(m.id)))
      OR t.steps @> jsonb_build_array(jsonb_build_object('avatar_media_id', m.id))
      OR EXISTS (SELECT FROM jsonb_array_elements(t.steps) st WHERE st->>'tag' = ANY (m.tags) AND (NOT m.group_only OR m.guild_id = t.guild_id)))
  WHERE m.deleted_at IS NULL
  UNION ALL
  SELECT m.id, 'message', g.name, coalesce(g.guild_id, '') FROM ` + from + ` m JOIN messages g ON g.deleted_at IS NULL
    AND (m.id = ANY (g.files)
      OR g.embeds @> jsonb_build_array(jsonb_build_object('image_media_id', m.id))
      OR g.embeds @> jsonb_build_array(jsonb_build_object('thumb_media_id', m.id)))
  WHERE m.deleted_at IS NULL`
}

// MediaByIDs returns the live files of ids.
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

// SetGroup puts files and messages into the server group guildID, "" for
// none. groupOnly keeps them from being suggested to other servers.
func (s *Store) SetGroup(ctx context.Context, guildID string, groupOnly bool, mediaIDs, messageIDs []int64) error {
	only := groupOnly && guildID != ""
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, table := range []struct {
		name string
		ids  []int64
	}{{"media", mediaIDs}, {"messages", messageIDs}} {
		if len(table.ids) == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE `+table.name+` SET guild_id = $2, group_only = $3 WHERE id = ANY ($1) AND deleted_at IS NULL`, table.ids, nullable(guildID), only); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// KnownGuilds are the servers of tasks, server groups or Discord grants, also
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
