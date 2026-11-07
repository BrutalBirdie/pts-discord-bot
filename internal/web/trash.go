package web

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
)

type trashRow struct {
	store.TrashItem
	DaysLeft  int    // calendar days until it is removed for good, 0 is today
	RestoreAs string // media whose name is taken by now come back under this name
}

type trashData struct {
	Rows   []trashRow
	Kind   string
	Counts map[string]int
	Days   int
	Vols   map[int64]int
	Paging paging
}

func (d trashData) Vol(id int64) int { return previewVolume(d.Vols, id) }

type trashRef struct {
	Kind string
	ID   int64
}

// parseTrashRef reads the "task.12" form used in ?undo= links.
func parseTrashRef(v string) *trashRef {
	kind, id, _ := strings.Cut(v, ".")
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 || (kind != store.TrashTask && kind != store.TrashMessage && kind != store.TrashMedia) {
		return nil
	}
	return &trashRef{Kind: kind, ID: n}
}

// daysLeft counts calendar days in the display timezone, so "tomorrow" matches the clock.
func daysLeft(deleted time.Time, days int, now time.Time) int {
	end := deleted.Add(time.Duration(days) * 24 * time.Hour).In(loc())
	d := math.Round(dayStart(end).Sub(dayStart(now.In(loc()))).Hours() / 24)
	return max(int(d), 0)
}

func (s *Server) trash(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	sc := sessionFrom(r).Scope()
	all, err := s.store.ListTrash(ctx, sc)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	q := r.URL.Query()
	d := trashData{Kind: q.Get("kind"), Counts: map[string]int{}, Days: settings.TrashRetentionDays, Paging: newPaging(r)}
	if _, ok := map[string]bool{"": true, store.TrashTask: true, store.TrashMessage: true, store.TrashMedia: true}[d.Kind]; !ok {
		d.Kind = ""
	}
	d.Paging.Keep("kind", d.Kind)
	d.Paging.Dated = true
	var items []store.TrashItem
	for _, it := range all {
		d.Counts[""]++
		d.Counts[it.Kind]++
		if (d.Kind == "" || d.Kind == it.Kind) && d.Paging.Matches(it.Name, it.DeletedBy) {
			items = append(items, it)
		}
	}
	items = window(&d.Paging, items)

	var taken map[string]bool
	now := time.Now()
	for _, it := range items {
		row := trashRow{TrashItem: it, DaysLeft: daysLeft(it.DeletedAt, d.Days, now)}
		if it.Kind == store.TrashMedia {
			if taken == nil {
				live, err := s.store.ListMedia(ctx, "", "")
				if err != nil {
					s.internalError(w, r, err)
					return
				}
				taken = make(map[string]bool, len(live))
				for _, m := range live {
					taken[m.Name] = true
				}
			}
			if taken[it.Name] {
				for n := 1; ; n++ {
					if name := store.RestoredName(it.Name, n); !taken[name] {
						row.RestoreAs = name
						break
					}
				}
			}
		}
		d.Rows = append(d.Rows, row)
	}
	var sounds []int64
	for _, it := range items {
		if it.Media != nil && it.Media.Kind == store.KindSound {
			sounds = append(sounds, it.ID)
		}
	}
	if d.Vols, err = s.store.PreviewVolumes(ctx, sessionFrom(r).Username, sounds); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "trash", d)
}

// removeItem moves an item to the trash, or deletes it right away when the
// trash is switched off. The caller checks that the item exists and is unused.
func (s *Server) removeItem(r *http.Request, kind string, id int64) (trashed bool, err error) {
	ctx := r.Context()
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return false, err
	}
	if settings.TrashRetentionDays > 0 {
		return true, s.store.Trash(ctx, kind, id, sessionFrom(r).Username)
	}
	switch kind {
	case store.TrashTask:
		return false, s.store.DeleteTask(ctx, id)
	case store.TrashMessage:
		return false, s.store.DeleteMessage(ctx, id)
	}
	if err := s.store.DeleteMedia(ctx, id); err != nil {
		return false, err
	}
	s.removeMediaFile(id)
	return false, nil
}

// removed redirects to the list after a delete, with an undo button when the item went to the trash.
func removed(w http.ResponseWriter, r *http.Request, path, kind string, id int64, trashed bool) {
	if !trashed {
		redirect(w, r, path, "deleted")
		return
	}
	redirect(w, r, path+"?undo="+kind+"."+strconv.FormatInt(id, 10), "trashed")
}

func trashDetail(trashed bool) string {
	if trashed {
		return "moved to the trash"
	}
	return ""
}

func (s *Server) removeMediaFile(id int64) {
	if err := os.Remove(s.cfg.MediaDir.Path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Error("removing media file", "id", id, "err", err)
	}
}

func (s *Server) trashedItem(w http.ResponseWriter, r *http.Request) (kind string, id int64, name string, ok bool) {
	kind = r.PathValue("kind")
	if id, ok = pathID(r); !ok {
		s.notFound(w, r)
		return
	}
	name, guildID, err := s.store.TrashedName(r.Context(), kind, id)
	if err == nil && kind == store.TrashTask && !sessionFrom(r).Scope().Has(guildID) {
		err = store.ErrNotFound
	}
	if err != nil {
		s.storeErr(w, r, err)
		return kind, id, name, false
	}
	return kind, id, name, true
}

func (s *Server) restoreTrash(w http.ResponseWriter, r *http.Request) {
	kind, id, name, ok := s.trashedItem(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	next := safeNext(r.PostForm.Get("next"), "/trash")
	var err error
	done, detail := "restored", ""
	switch kind {
	case store.TrashTask:
		err = s.store.RestoreTask(ctx, id, sessionFrom(r).Username)
		done = "restored-task"
	case store.TrashMessage:
		err = s.store.RestoreMessage(ctx, id)
	case store.TrashMedia:
		var as string
		if as, err = s.store.RestoreMedia(ctx, id); err == nil && as != name {
			sep := "?"
			if strings.Contains(next, "?") {
				sep = "&"
			}
			next += sep + "as=" + url.QueryEscape(as)
			done, detail = "renamed", "as "+as
		}
	}
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.audit(r, kind+".restore", name, detail)
	redirect(w, r, next, done)
}

func (s *Server) purgeTrash(w http.ResponseWriter, r *http.Request) {
	kind, id, name, ok := s.trashedItem(w, r)
	if !ok {
		return
	}
	if err := s.store.PurgeTrashed(r.Context(), kind, id); err != nil {
		s.storeErr(w, r, err)
		return
	}
	if kind == store.TrashMedia {
		s.removeMediaFile(id)
	}
	s.audit(r, kind+".purge", name, "")
	redirect(w, r, "/trash", "purged")
}

func (s *Server) emptyTrash(w http.ResponseWriter, r *http.Request) {
	n, ids, err := s.store.PurgeTrash(r.Context(), 0)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, id := range ids {
		s.removeMediaFile(id)
	}
	s.audit(r, "trash.empty", "trash", strconv.Itoa(n)+" items")
	redirect(w, r, "/trash", "emptied")
}
