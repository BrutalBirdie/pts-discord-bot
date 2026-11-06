package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// ProfileLimits returns the last change and any rate limit of each profile field of the bot in a server.
func (s *Store) ProfileLimits(ctx context.Context, guildID string) (map[string]tasks.ProfileLimit, error) {
	rows, err := s.pool.Query(ctx, `SELECT field, changed_at, blocked_until FROM bot_profile_changes WHERE guild_id = $1`, guildID)
	if err != nil {
		return nil, err
	}
	out := map[string]tasks.ProfileLimit{}
	var field string
	var changed, blocked *time.Time
	_, err = pgx.ForEachRow(rows, []any{&field, &changed, &blocked}, func() error {
		var l tasks.ProfileLimit
		if changed != nil {
			l.ChangedAt = *changed
		}
		if blocked != nil {
			l.BlockedUntil = *blocked
		}
		out[field] = l
		return nil
	})
	return out, err
}

// RecordProfileChange notes a successful change of the fields.
func (s *Store) RecordProfileChange(ctx context.Context, guildID string, fields []string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO bot_profile_changes (guild_id, field, changed_at) SELECT $1, unnest($2::text[]), $3
ON CONFLICT (guild_id, field) DO UPDATE SET changed_at = EXCLUDED.changed_at`, guildID, fields, at)
	return err
}

// BlockProfile notes that Discord refuses changes of the fields until the given time.
func (s *Store) BlockProfile(ctx context.Context, guildID string, fields []string, until time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO bot_profile_changes (guild_id, field, blocked_until) SELECT $1, unnest($2::text[]), $3
ON CONFLICT (guild_id, field) DO UPDATE SET blocked_until = EXCLUDED.blocked_until`, guildID, fields, until)
	return err
}
