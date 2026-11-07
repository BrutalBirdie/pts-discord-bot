package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// Kinds of trashed items, also used in the /trash URLs.
const (
	TrashTask    = "task"
	TrashMessage = "message"
	TrashMedia   = "media"
)

var trashTables = map[string]string{TrashTask: "tasks", TrashMessage: "messages", TrashMedia: "media"}

func trashTable(kind string) (string, error) {
	t, ok := trashTables[kind]
	if !ok {
		return "", ErrNotFound
	}
	return t, nil
}

// TrashItem is a deleted task, message or media file. One of Task, Message and Media is set.
type TrashItem struct {
	Kind      string
	ID        int64
	Name      string
	DeletedAt time.Time
	DeletedBy string
	GuildID   string // server of a task, server group of a message or file
	Task      *tasks.Task
	Message   *messages.Message
	Media     *Media
}

// Trash moves an item to the trash. A task must also be unscheduled by the caller.
func (s *Store) Trash(ctx context.Context, kind string, id int64, by string) error {
	table, err := trashTable(kind)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE `+table+` SET deleted_at = now(), deleted_by = $2 WHERE id = $1 AND deleted_at IS NULL`, id, by)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ListTrash returns the trashed tasks in scope and every trashed message and file, the newest deletion first.
func (s *Store) ListTrash(ctx context.Context, sc Scope) ([]TrashItem, error) {
	var out []TrashItem
	rows, err := s.pool.Query(ctx, `SELECT `+taskCols+`, deleted_at, coalesce(deleted_by, '') FROM tasks WHERE deleted_at IS NOT NULL AND `+inScope("guild_id", "1"), sc.param())
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (TrashItem, error) {
		var t tasks.Task
		it := TrashItem{Kind: TrashTask, Task: &t}
		err := r.Scan(&t.ID, &t.Name, &t.GuildID, &t.Enabled, &t.Busy, &t.Slash, &t.Trigger, &t.Steps, &t.PausedReason, &t.CreatedBy, &t.UpdatedBy, &t.UpdatedAt, &it.DeletedAt, &it.DeletedBy)
		it.ID, it.Name, it.GuildID = t.ID, t.Name, t.GuildID
		return it, err
	})
	if err != nil {
		return nil, err
	}
	out = append(out, list...)

	rows, err = s.pool.Query(ctx, `SELECT `+messageCols+`, deleted_at, coalesce(deleted_by, '') FROM messages WHERE deleted_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	list, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (TrashItem, error) {
		var m messages.Message
		it := TrashItem{Kind: TrashMessage, Message: &m}
		err := r.Scan(append(messageFields(&m), &it.DeletedAt, &it.DeletedBy)...)
		it.ID, it.Name, it.GuildID = m.ID, m.Name, m.GuildID
		return it, err
	})
	if err != nil {
		return nil, err
	}
	out = append(out, list...)

	rows, err = s.pool.Query(ctx, `SELECT `+mediaCols+`, deleted_at, coalesce(deleted_by, '') FROM media WHERE deleted_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	list, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (TrashItem, error) {
		var m Media
		it := TrashItem{Kind: TrashMedia, Media: &m}
		err := r.Scan(append(m.fields(), &it.DeletedAt, &it.DeletedBy)...)
		it.ID, it.Name, it.GuildID = m.ID, m.Name, m.GuildID
		return it, err
	})
	if err != nil {
		return nil, err
	}
	out = append(out, list...)

	slices.SortStableFunc(out, func(a, b TrashItem) int {
		if c := b.DeletedAt.Compare(a.DeletedAt); c != 0 {
			return c
		}
		return int(b.ID - a.ID)
	})
	return out, nil
}

func (s *Store) CountTrash(ctx context.Context, sc Scope) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM tasks WHERE deleted_at IS NOT NULL AND `+inScope("guild_id", "1")+`)
  + (SELECT count(*) FROM messages WHERE deleted_at IS NOT NULL)
  + (SELECT count(*) FROM media WHERE deleted_at IS NOT NULL)`, sc.param()).Scan(&n)
	return n, err
}

// TrashedName returns the name and server or server group of a trashed item.
func (s *Store) TrashedName(ctx context.Context, kind string, id int64) (name, guildID string, err error) {
	table, err := trashTable(kind)
	if err != nil {
		return "", "", err
	}
	err = s.pool.QueryRow(ctx, `SELECT name, coalesce(guild_id, '') FROM `+table+` WHERE id = $1 AND deleted_at IS NOT NULL`, id).Scan(&name, &guildID)
	return name, guildID, noRows(err)
}

// RestoreTask takes a task out of the trash, paused so it does not fire unexpectedly.
func (s *Store) RestoreTask(ctx context.Context, id int64, by string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE tasks SET deleted_at = NULL, deleted_by = NULL, enabled = false, paused_reason = '', updated_by = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NOT NULL`, id, by)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) RestoreMessage(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE messages SET deleted_at = NULL, deleted_by = NULL WHERE id = $1 AND deleted_at IS NOT NULL`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// RestoredName is the name tried on the n-th attempt to restore a file whose name is taken:
// "intro.ogg", "intro (restored).ogg", "intro (restored 2).ogg".
func RestoredName(name string, n int) string {
	if n == 0 {
		return name
	}
	ext := filepath.Ext(name)
	if ext == name || strings.ContainsAny(ext, " ()") {
		ext = ""
	}
	suffix := " (restored)"
	if n > 1 {
		suffix = fmt.Sprintf(" (restored %d)", n)
	}
	return strings.TrimSuffix(name, ext) + suffix + ext
}

// RestoreMedia takes a file out of the trash. If another file has its name
// meanwhile, it gets the first free RestoredName, which is returned.
func (s *Store) RestoreMedia(ctx context.Context, id int64) (string, error) {
	name, _, err := s.TrashedName(ctx, TrashMedia, id)
	if err != nil {
		return "", err
	}
	for n := 0; n < 100; n++ {
		try := RestoredName(name, n)
		tag, err := s.pool.Exec(ctx, `UPDATE media SET deleted_at = NULL, deleted_by = NULL, name = $2 WHERE id = $1 AND deleted_at IS NOT NULL`, id, try)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			continue
		}
		if err == nil && tag.RowsAffected() == 0 {
			return "", ErrNotFound
		}
		return try, err
	}
	return "", ErrNameTaken
}

// PurgeTrashed deletes a trashed item for good. The caller removes the file of media.
func (s *Store) PurgeTrashed(ctx context.Context, kind string, id int64) error {
	table, err := trashTable(kind)
	if err != nil {
		return err
	}
	var n int
	err = s.pool.QueryRow(ctx, `WITH d AS (DELETE FROM `+table+` WHERE id = $1 AND deleted_at IS NOT NULL RETURNING id),
v AS (DELETE FROM preview_volume WHERE $2 AND media_id IN (SELECT id FROM d))
SELECT count(*) FROM d`, id, kind == TrashMedia).Scan(&n)
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// PurgeTrash deletes the items trashed before the retention and returns how
// many there were and the IDs of the media files, which the caller removes.
func (s *Store) PurgeTrash(ctx context.Context, retention time.Duration) (int, []int64, error) {
	secs := int(retention.Seconds())
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	var n int
	for _, table := range []string{"tasks", "messages"} {
		tag, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE deleted_at <= now() - $1 * interval '1 second'`, secs)
		if err != nil {
			return 0, nil, err
		}
		n += int(tag.RowsAffected())
	}
	rows, err := tx.Query(ctx, `DELETE FROM media WHERE deleted_at <= now() - $1 * interval '1 second' RETURNING id`, secs)
	if err != nil {
		return 0, nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM preview_volume WHERE media_id = ANY ($1)`, ids); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return n + len(ids), ids, nil
}
