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
	GuildID     string   // server group, "" for none
	GroupOnly   bool     // not suggested to other servers than GuildID
	UsedBy      []string // names of tasks and messages in scope that reference it, filled by FindMedia
	UsedElse    int      // tasks and messages out of scope that reference it
}

const mediaCols = `id, name, kind, content_type, size_bytes, duration_ms, width, height, uploaded_by, created_at, tags, coalesce(guild_id, ''), group_only`

func (m *Media) fields() []any {
	return []any{&m.ID, &m.Name, &m.Kind, &m.ContentType, &m.SizeBytes, &m.DurationMS, &m.Width, &m.Height, &m.UploadedBy, &m.CreatedAt, &m.Tags, &m.GuildID, &m.GroupOnly}
}

func scanMedia(r pgx.Row) (Media, error) {
	var m Media
	err := r.Scan(m.fields()...)
	return m, err
}

func nullable(guildID string) *string {
	if guildID == "" {
		return nil
	}
	return &guildID
}

// UpdateMedia renames a file and sets its tags and server group.
func (s *Store) UpdateMedia(ctx context.Context, m Media) error {
	tags := m.Tags
	if tags == nil {
		tags = []string{}
	}
	tag, err := s.pool.Exec(ctx, `UPDATE media SET name = $2, tags = $3, guild_id = $4, group_only = $5 WHERE id = $1 AND deleted_at IS NULL`,
		m.ID, m.Name, tags, nullable(m.GuildID), m.GroupOnly && m.GuildID != "")
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrNameTaken
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Tags returns the tags of the sounds and videos offered to a server, the ones its Play steps can pick from.
func (s *Store) Tags(ctx context.Context, guildID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT unnest(tags) AS t FROM media m WHERE kind IN ('sound', 'video') AND deleted_at IS NULL AND `+offered("m", "1")+` ORDER BY t`, guildID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// RandomSound picks a sound or video with the tag offered to a server.
func (s *Store) RandomSound(ctx context.Context, guildID, tag string) (Media, error) {
	m, err := scanMedia(s.pool.QueryRow(ctx, `SELECT `+mediaCols+` FROM media m WHERE kind IN ('sound', 'video') AND deleted_at IS NULL AND $1 = ANY (tags) AND `+offered("m", "2")+`
ORDER BY random() LIMIT 1`, tag, guildID))
	return m, noRows(err)
}

func (s *Store) AddMedia(ctx context.Context, m Media) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO media (name, kind, content_type, size_bytes, duration_ms, width, height, uploaded_by, guild_id, group_only)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`, m.Name, m.Kind, m.ContentType, m.SizeBytes, m.DurationMS, m.Width, m.Height, m.UploadedBy, nullable(m.GuildID), m.GroupOnly && m.GuildID != "").Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return 0, ErrNameTaken
	}
	return id, err
}

func (s *Store) Media(ctx context.Context, id int64) (Media, error) {
	m, err := scanMedia(s.pool.QueryRow(ctx, `SELECT `+mediaCols+` FROM media WHERE id = $1 AND deleted_at IS NULL`, id))
	return m, noRows(err)
}

// AnyMedia also finds trashed media, so the Trash can preview them.
func (s *Store) AnyMedia(ctx context.Context, id int64) (Media, error) {
	m, err := scanMedia(s.pool.QueryRow(ctx, `SELECT `+mediaCols+` FROM media WHERE id = $1`, id))
	return m, noRows(err)
}

// MediaByName finds a file by its name, which is unique.
func (s *Store) MediaByName(ctx context.Context, name string) (Media, error) {
	m, err := scanMedia(s.pool.QueryRow(ctx, `SELECT `+mediaCols+` FROM media WHERE name = $1 AND deleted_at IS NULL`, name))
	return m, noRows(err)
}

// listMediaQuery fills UsedBy with the tasks and messages in scope that
// reference a file and counts the others, whose names stay hidden. Messages
// have no scope, their names always show.
var listMediaQuery = `WITH users AS (
  SELECT media_id, kind || ' ' || name AS name, owner, kind = 'message' AS open FROM (` + mediaUsers("media") + `) u
)
SELECT ` + mediaCols + `,
  ARRAY(SELECT u.name FROM users u WHERE u.media_id = m.id AND (u.open OR ` + inScope("u.owner", "6") + `) ORDER BY u.name),
  (SELECT count(*) FROM users u WHERE u.media_id = m.id AND NOT (u.open OR ` + inScope("u.owner", "6") + `))
FROM media m WHERE m.deleted_at IS NULL AND ` + offered("m", "7") + ` AND ` + inGroup("m", "8") + ` AND ($1 = '' OR kind = $1) AND ($2 = '' OR $2 = ANY (tags))
  AND ($3 = '' OR name ILIKE '%' || $3 || '%' OR array_to_string(tags, ' ') ILIKE '%' || $3 || '%')
ORDER BY ` + ownFirst("m", "7") + `, kind, lower(name), id LIMIT NULLIF($4, 0) OFFSET $5`

// MediaQuery filters the media library. Search matches names and tags, a zero
// Limit lists all. Scope limits the task names in UsedBy. Offer lists only
// the files offered to that server, its own first; Group only those of a
// server group, NoGroup for files without one.
type MediaQuery struct {
	Scope             Scope
	Offer, Group      string
	Kind, Tag, Search string
	Limit, Offset     int
}

// ListMedia returns the media of a kind offered to a server, every kind when
// kind is empty and all media when guildID is empty.
func (s *Store) ListMedia(ctx context.Context, guildID, kind string) ([]Media, error) {
	return s.FindMedia(ctx, MediaQuery{Scope: All, Offer: guildID, Kind: kind})
}

func (s *Store) FindMedia(ctx context.Context, q MediaQuery) ([]Media, error) {
	rows, err := s.pool.Query(ctx, listMediaQuery, q.Kind, q.Tag, likeEscaper.Replace(q.Search), q.Limit, q.Offset, q.Scope.param(), q.Offer, q.Group)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Media, error) {
		var m Media
		err := r.Scan(append(m.fields(), &m.UsedBy, &m.UsedElse)...)
		return m, err
	})
}

func (s *Store) DeleteMedia(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `WITH v AS (DELETE FROM preview_volume WHERE media_id = $1) DELETE FROM media WHERE id = $1`, id)
	return err
}

// SearchSounds finds the sounds and videos offered to a server by name: the
// server's own first, then names starting with q. A file another server keeps
// to itself is found by its exact name.
func (s *Store) SearchSounds(ctx context.Context, guildID, q string, limit int) ([]Media, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+mediaCols+` FROM media m WHERE kind IN ('sound', 'video') AND deleted_at IS NULL
  AND (name ILIKE '%' || $1 || '%' AND `+offered("m", "3")+` OR $4 <> '' AND lower(name) = lower($4))
ORDER BY `+ownFirst("m", "3")+`, name NOT ILIKE $1 || '%', lower(name) LIMIT $2`, likeEscaper.Replace(q), limit, guildID, q)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Media, error) { return scanMedia(r) })
}

// PreviewVolumes returns the saved preview volumes of a user, sounds without one are missing.
func (s *Store) PreviewVolumes(ctx context.Context, username string, ids []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT media_id, volume FROM preview_volume WHERE username = $1 AND media_id = ANY ($2)`, username, ids)
	if err != nil {
		return nil, err
	}
	var id int64
	var vol int
	_, err = pgx.ForEachRow(rows, []any{&id, &vol}, func() error {
		out[id] = vol
		return nil
	})
	return out, err
}

func (s *Store) SetPreviewVolume(ctx context.Context, username string, mediaID int64, volume int) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO preview_volume (username, media_id, volume) SELECT $1, id, $3 FROM media WHERE id = $2
ON CONFLICT (username, media_id) DO UPDATE SET volume = EXCLUDED.volume`, username, mediaID, volume)
	return err
}

// MediaSummary is the whole library at a glance: files per kind ("" for all), their size and every tag.
type MediaSummary struct {
	Counts map[string]int
	Bytes  int64
	Tags   []string
}

func (s *Store) MediaSummary(ctx context.Context) (MediaSummary, error) {
	out := MediaSummary{Counts: map[string]int{}}
	rows, err := s.pool.Query(ctx, `SELECT kind, count(*), coalesce(sum(size_bytes), 0) FROM media WHERE deleted_at IS NULL GROUP BY kind`)
	if err != nil {
		return out, err
	}
	var kind string
	var n int
	var size int64
	if _, err = pgx.ForEachRow(rows, []any{&kind, &n, &size}, func() error {
		out.Counts[kind] = n
		out.Counts[""] += n
		out.Bytes += size
		return nil
	}); err != nil {
		return out, err
	}
	rows, err = s.pool.Query(ctx, `SELECT DISTINCT unnest(tags) AS t FROM media WHERE deleted_at IS NULL ORDER BY t`)
	if err != nil {
		return out, err
	}
	out.Tags, err = pgx.CollectRows(rows, pgx.RowTo[string])
	return out, err
}

func (s *Store) MediaBytes(ctx context.Context) (count int, bytes int64, err error) {
	err = s.pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(size_bytes), 0) FROM media WHERE deleted_at IS NULL`).Scan(&count, &bytes)
	return count, bytes, err
}
