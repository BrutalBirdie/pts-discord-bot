package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	KindSound = "sound"
	KindImage = "image"
	KindVideo = "video"
	KindFile  = "file"
)

var ErrNameTaken = errors.New("name taken")

type Media struct {
	ID          int64
	Name        string
	Kind        string
	ContentType string
	SizeBytes   int64
	DurationMS  int
	Width       int
	Height      int
	UploadedBy  string
	CreatedAt   time.Time
	Tags        []string
	UsedBy      []string // names of tasks and messages that reference it, filled by ListMedia
}

const mediaCols = `id, name, kind, content_type, size_bytes, duration_ms, width, height, uploaded_by, created_at, tags`

func scanMedia(r pgx.Row) (Media, error) {
	var m Media
	err := r.Scan(&m.ID, &m.Name, &m.Kind, &m.ContentType, &m.SizeBytes, &m.DurationMS, &m.Width, &m.Height, &m.UploadedBy, &m.CreatedAt, &m.Tags)
	return m, err
}

// UpdateMedia renames a file and sets its tags.
func (s *Store) UpdateMedia(ctx context.Context, id int64, name string, tags []string) error {
	if tags == nil {
		tags = []string{}
	}
	tag, err := s.pool.Exec(ctx, `UPDATE media SET name = $2, tags = $3 WHERE id = $1`, id, name, tags)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrNameTaken
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Tags returns every tag of sounds and videos, the ones Play steps can pick from.
func (s *Store) Tags(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT unnest(tags) AS t FROM media WHERE kind IN ('sound', 'video') ORDER BY t`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// RandomSound picks a sound or video with the tag.
func (s *Store) RandomSound(ctx context.Context, tag string) (Media, error) {
	m, err := scanMedia(s.pool.QueryRow(ctx, `SELECT `+mediaCols+` FROM media WHERE kind IN ('sound', 'video') AND $1 = ANY (tags) ORDER BY random() LIMIT 1`, tag))
	return m, noRows(err)
}

func (s *Store) AddMedia(ctx context.Context, m Media) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO media (name, kind, content_type, size_bytes, duration_ms, width, height, uploaded_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`, m.Name, m.Kind, m.ContentType, m.SizeBytes, m.DurationMS, m.Width, m.Height, m.UploadedBy).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return 0, ErrNameTaken
	}
	return id, err
}

func (s *Store) Media(ctx context.Context, id int64) (Media, error) {
	m, err := scanMedia(s.pool.QueryRow(ctx, `SELECT `+mediaCols+` FROM media WHERE id = $1`, id))
	return m, noRows(err)
}

func (s *Store) MediaByName(ctx context.Context, name string) (Media, error) {
	m, err := scanMedia(s.pool.QueryRow(ctx, `SELECT `+mediaCols+` FROM media WHERE name = $1`, name))
	return m, noRows(err)
}

// ListMedia returns all media of a kind, every kind when kind is empty.
func (s *Store) ListMedia(ctx context.Context, kind string) ([]Media, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+mediaCols+`,
  ARRAY(SELECT 'task ' || t.name FROM tasks t WHERE t.steps @> jsonb_build_array(jsonb_build_object('media_id', m.id))
           OR EXISTS (SELECT FROM jsonb_array_elements(t.steps) st WHERE st->>'tag' = ANY (m.tags))
        UNION ALL
        SELECT 'message ' || g.name FROM messages g
        WHERE m.id = ANY (g.files)
           OR g.embeds @> jsonb_build_array(jsonb_build_object('image_media_id', m.id))
           OR g.embeds @> jsonb_build_array(jsonb_build_object('thumb_media_id', m.id)))
FROM media m WHERE $1 = '' OR kind = $1 ORDER BY kind, lower(name)`, kind)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Media, error) {
		var m Media
		err := r.Scan(&m.ID, &m.Name, &m.Kind, &m.ContentType, &m.SizeBytes, &m.DurationMS, &m.Width, &m.Height, &m.UploadedBy, &m.CreatedAt, &m.Tags, &m.UsedBy)
		return m, err
	})
}

func (s *Store) DeleteMedia(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM media WHERE id = $1`, id)
	return err
}

func (s *Store) MediaBytes(ctx context.Context) (count int, bytes int64, err error) {
	err = s.pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(size_bytes), 0) FROM media`).Scan(&count, &bytes)
	return count, bytes, err
}
