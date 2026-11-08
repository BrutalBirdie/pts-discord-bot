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
	DisplayName string // shown instead of Name when set
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
	Emoji       string   // shown on the soundboard
	IconID      int64    // an image shown on the soundboard instead of Emoji
	Volume      int      // percent for every playback, 0 for the volume of the step or soundboard
	TrimStartMS int      // playback starts here
	TrimEndMS   int      // and ends here, 0 for the end of the file
	Batch       string   // the files uploaded together share it, for reviewing them
	UsedBy      []string // names of tasks and messages in scope that reference it, filled by FindMedia
	UsedElse    int      // tasks and messages out of scope that reference it
}

const mediaCols = `id, name, kind, content_type, size_bytes, duration_ms, width, height, uploaded_by, created_at, tags, coalesce(guild_id, ''), group_only, emoji, coalesce(icon_media_id, 0), volume, trim_start_ms, trim_end_ms, display_name, upload_batch`

func (m *Media) fields() []any {
	return []any{&m.ID, &m.Name, &m.Kind, &m.ContentType, &m.SizeBytes, &m.DurationMS, &m.Width, &m.Height, &m.UploadedBy, &m.CreatedAt, &m.Tags, &m.GuildID, &m.GroupOnly, &m.Emoji, &m.IconID, &m.Volume, &m.TrimStartMS, &m.TrimEndMS, &m.DisplayName, &m.Batch}
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
	tag, err := s.pool.Exec(ctx, `UPDATE media SET name = $2, tags = $3, guild_id = $4, group_only = $5, display_name = $6 WHERE id = $1 AND deleted_at IS NULL`,
		m.ID, m.Name, tags, nullable(m.GuildID), m.GroupOnly && m.GuildID != "", m.DisplayName)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrNameTaken
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Label is the display name, or the file name without one.
func (m Media) Label() string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Name
}

// PlayMS is how long a playback of the file lasts, 0 when unknown.
func (m Media) PlayMS() int {
	end := m.DurationMS
	if m.TrimEndMS > 0 && (end == 0 || m.TrimEndMS < end) {
		end = m.TrimEndMS
	}
	return max(end-m.TrimStartMS, 0)
}

// SoundEdit is what the Edit sound dialog changes.
type SoundEdit struct {
	ID          int64
	Name        string
	DisplayName string
	Tags        []string
	Emoji       string
	IconID      int64
	Volume      int
	TrimStartMS int
	TrimEndMS   int
}

func (s *Store) EditSound(ctx context.Context, e SoundEdit) error {
	var icon *int64
	if e.IconID != 0 {
		icon = &e.IconID
	}
	tag, err := s.pool.Exec(ctx, `UPDATE media SET name = $2, emoji = $3, icon_media_id = $4, volume = $5, trim_start_ms = $6, trim_end_ms = $7, display_name = $8,
  tags = coalesce($9, tags)
WHERE id = $1 AND deleted_at IS NULL`, e.ID, e.Name, e.Emoji, icon, e.Volume, e.TrimStartMS, e.TrimEndMS, e.DisplayName, e.Tags)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrNameTaken
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Favorites returns the IDs of the sounds a user starred.
func (s *Store) Favorites(ctx context.Context, username string) (map[int64]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT media_id FROM favorite_media WHERE username = $1`, username)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

// FavoriteSounds returns the starred sounds of a user that are not trashed, by name.
func (s *Store) FavoriteSounds(ctx context.Context, username string) ([]Media, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+mediaCols+` FROM media m JOIN favorite_media f ON f.media_id = m.id
WHERE f.username = $1 AND m.deleted_at IS NULL ORDER BY lower(m.name)`, username)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Media, error) { return scanMedia(r) })
}

func (s *Store) SetFavorite(ctx context.Context, username string, mediaID int64, on bool) error {
	if !on {
		_, err := s.pool.Exec(ctx, `DELETE FROM favorite_media WHERE username = $1 AND media_id = $2`, username, mediaID)
		return err
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO favorite_media (username, media_id) SELECT $1, id FROM media WHERE id = $2 AND deleted_at IS NULL
ON CONFLICT DO NOTHING`, username, mediaID)
	if err == nil && tag.RowsAffected() == 0 {
		if _, err := s.Media(ctx, mediaID); err != nil {
			return err
		}
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

// SoundTag is a tag of the sounds offered to a server, with how many sounds have it and its icon.
type SoundTag struct {
	Name   string
	Count  int
	Emoji  string
	IconID int64
}

// SoundTags returns the tags of the sounds offered to a server by name. Count
// is the number of sounds with the tag among those that have all of within.
func (s *Store) SoundTags(ctx context.Context, guildID string, within []string) ([]SoundTag, error) {
	if within == nil {
		within = []string{}
	}
	rows, err := s.pool.Query(ctx, `SELECT t.tag, t.n, coalesce(l.emoji, ''), coalesce(l.icon_media_id, 0)
FROM (SELECT unnest(tags) AS tag, count(*) AS n FROM media m WHERE kind = 'sound' AND deleted_at IS NULL AND `+offered("m", "1")+` AND tags @> $2 GROUP BY 1) t
LEFT JOIN tag_looks l ON l.tag = t.tag ORDER BY t.tag`, guildID, within)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SoundTag, error) {
		var t SoundTag
		err := r.Scan(&t.Name, &t.Count, &t.Emoji, &t.IconID)
		return t, err
	})
}

// SetTagLook sets the emoji or image shown for a tag, both empty remove it.
func (s *Store) SetTagLook(ctx context.Context, tag, emoji string, iconID int64) error {
	if emoji == "" && iconID == 0 {
		_, err := s.pool.Exec(ctx, `DELETE FROM tag_looks WHERE tag = $1`, tag)
		return err
	}
	var icon *int64
	if iconID != 0 {
		icon = &iconID
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO tag_looks (tag, emoji, icon_media_id) VALUES ($1, $2, $3)
ON CONFLICT (tag) DO UPDATE SET emoji = EXCLUDED.emoji, icon_media_id = EXCLUDED.icon_media_id`, tag, emoji, icon)
	return err
}

// RandomSound picks a sound or video with the tag offered to a server.
func (s *Store) RandomSound(ctx context.Context, guildID, tag string) (Media, error) {
	m, err := scanMedia(s.pool.QueryRow(ctx, `SELECT `+mediaCols+` FROM media m WHERE kind IN ('sound', 'video') AND deleted_at IS NULL AND $1 = ANY (tags) AND `+offered("m", "2")+`
ORDER BY random() LIMIT 1`, tag, guildID))
	return m, noRows(err)
}

func (s *Store) AddMedia(ctx context.Context, m Media) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO media (name, kind, content_type, size_bytes, duration_ms, width, height, uploaded_by, guild_id, group_only, upload_batch)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`, m.Name, m.Kind, m.ContentType, m.SizeBytes, m.DurationMS, m.Width, m.Height, m.UploadedBy, nullable(m.GuildID), m.GroupOnly && m.GuildID != "", m.Batch).Scan(&id)
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
FROM media m WHERE m.deleted_at IS NULL AND ` + offered("m", "7") + ` AND ` + inGroup("m", "8") + ` AND ($1 = '' OR kind = $1) AND ($2 = '' OR $2 = ANY (tags)) AND ($9::text[] IS NULL OR tags @> $9)
  AND ($10 = '' OR upload_batch = $10)
  AND ($3 = '' OR name ILIKE '%' || $3 || '%' OR display_name ILIKE '%' || $3 || '%' OR array_to_string(tags, ' ') ILIKE '%' || $3 || '%')
ORDER BY ` + ownFirst("m", "7") + `, kind, lower(name), id LIMIT NULLIF($4, 0) OFFSET $5`

// MediaQuery filters the media library. Search matches names and tags, a zero
// Limit lists all. Scope limits the task names in UsedBy. Offer lists only
// the files offered to that server, its own first; Group only those of a
// server group, NoGroup for files without one.
type MediaQuery struct {
	Scope             Scope
	Offer, Group      string
	Kind, Tag, Search string
	AllTags           []string // only files with all of these tags
	Batch             string   // only the files of one upload
	Limit, Offset     int
}

// ListMedia returns the media of a kind offered to a server, every kind when
// kind is empty and all media when guildID is empty.
func (s *Store) ListMedia(ctx context.Context, guildID, kind string) ([]Media, error) {
	return s.FindMedia(ctx, MediaQuery{Scope: All, Offer: guildID, Kind: kind})
}

func (s *Store) FindMedia(ctx context.Context, q MediaQuery) ([]Media, error) {
	rows, err := s.pool.Query(ctx, listMediaQuery, q.Kind, q.Tag, likeEscaper.Replace(q.Search), q.Limit, q.Offset, q.Scope.param(), q.Offer, q.Group, q.AllTags, q.Batch)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Media, error) {
		var m Media
		err := r.Scan(append(m.fields(), &m.UsedBy, &m.UsedElse)...)
		return m, err
	})
}

// AddTags adds tags to files and keeps the tags they have.
func (s *Store) AddTags(ctx context.Context, ids []int64, tags []string) error {
	_, err := s.pool.Exec(ctx, `UPDATE media SET tags = ARRAY(SELECT DISTINCT t FROM unnest(tags || $2::text[]) t ORDER BY t)
WHERE id = ANY ($1) AND deleted_at IS NULL`, ids, tags)
	return err
}

func (s *Store) DeleteMedia(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM media WHERE id = $1`, id)
	return err
}

// SearchSounds finds the sounds and videos offered to a server by name: the
// server's own first, then names starting with q. A file another server keeps
// to itself is found by its exact name.
func (s *Store) SearchSounds(ctx context.Context, guildID, q string, limit int) ([]Media, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+mediaCols+` FROM media m WHERE kind IN ('sound', 'video') AND deleted_at IS NULL
  AND ((name ILIKE '%' || $1 || '%' OR display_name ILIKE '%' || $1 || '%') AND `+offered("m", "3")+` OR $4 <> '' AND (lower(name) = lower($4) OR lower(display_name) = lower($4)))
ORDER BY `+ownFirst("m", "3")+`, coalesce(nullif(display_name, ''), name) NOT ILIKE $1 || '%', lower(coalesce(nullif(display_name, ''), name)) LIMIT $2`, likeEscaper.Replace(q), limit, guildID, q)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Media, error) { return scanMedia(r) })
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
