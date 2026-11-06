package web

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/media"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
)

type voiceGroup struct {
	Guild bot.Guild
	Voice []bot.Channel
}

type mediaData struct {
	Items       []store.Media
	Kind        string
	Tag         string
	Tags        []string
	Counts      map[string]int
	TotalBytes  int64
	Voice       []voiceGroup
	MaxUploadMB int
	Vols        map[int64]int // preview volumes of the listed sounds
	Owners      []bot.Guild   // servers an upload can belong to
	Shared      bool          // uploads can be shared by all servers
	Names       map[string]string
	Paging      paging
}

// Owner is the name of the server owning a file, empty for shared files.
func (d mediaData) Owner(m store.Media) string {
	if m.GuildID == "" {
		return ""
	}
	if n := d.Names[m.GuildID]; n != "" {
		return n
	}
	return m.GuildID
}

func (d mediaData) Vol(id int64) int { return previewVolume(d.Vols, id) }

func (s *Server) voiceGroups(r *http.Request) []voiceGroup {
	var out []voiceGroup
	for _, g := range s.guilds(r) {
		vg := voiceGroup{Guild: g}
		for _, c := range s.bot.Channels(g.ID) {
			if c.Voice && c.Usable {
				vg.Voice = append(vg.Voice, c)
			}
		}
		if len(vg.Voice) > 0 {
			out = append(out, vg)
		}
	}
	return out
}

func (s *Server) mediaPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess := sessionFrom(r)
	sum, err := s.store.MediaSummary(ctx, sess.Scope())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	q := r.URL.Query()
	d := mediaData{Kind: q.Get("kind"), Tag: q.Get("tag"), Counts: sum.Counts, TotalBytes: sum.Bytes, Tags: sum.Tags, Voice: s.voiceGroups(r), MaxUploadMB: settings.MaxUploadMB, Paging: newPaging(r),
		Owners: s.ownerChoices(r, ""), Shared: !sess.Discord(), Names: map[string]string{}}
	for _, g := range d.Owners {
		d.Names[g.ID] = g.Name
	}
	d.Paging.Keep("kind", d.Kind)
	d.Paging.Keep("tag", d.Tag)
	items, err := s.store.FindMedia(ctx, store.MediaQuery{Scope: sess.Scope(), Kind: d.Kind, Tag: d.Tag, Search: d.Paging.Query, Limit: d.Paging.Limit(), Offset: d.Paging.Offset()})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	d.Items = cut(&d.Paging, items)
	var sounds []int64
	for _, m := range d.Items {
		if m.Kind == store.KindSound {
			sounds = append(sounds, m.ID)
		}
	}
	if d.Vols, err = s.store.PreviewVolumes(ctx, sessionFrom(r).Username, sounds); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "media", d)
}

// upload streams the files of a multipart form into the media directory.
// The csrf field must come before the files.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess := sessionFrom(r)
	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	limit := int64(settings.MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "Send the files as a form upload.")
		return
	}
	csrfOK := false
	owner := ""
	var added, problems []string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				problems = append(problems, fmt.Sprintf("The upload is larger than %d MB.", settings.MaxUploadMB))
			} else {
				problems = append(problems, "The upload was interrupted: "+err.Error())
			}
			break
		}
		if part.FormName() == "csrf" {
			token, _ := io.ReadAll(io.LimitReader(part, 256))
			csrfOK = s.validCSRF(r, sess, string(token))
			continue
		}
		if part.FormName() == "guild" {
			v, _ := io.ReadAll(io.LimitReader(part, 64))
			owner = string(v)
			continue
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		if !csrfOK {
			s.fail(w, r, http.StatusForbidden, "The request could not be verified. Reload the page and try again.")
			return
		}
		if !validOwner(sess, owner) {
			s.notFound(w, r)
			return
		}
		name, err := media.CleanName(part.FileName())
		if err != nil {
			problems = append(problems, part.FileName()+": "+err.Error()+".")
			continue
		}
		if err := s.saveUpload(r, part, name, owner, limit); err != nil {
			problems = append(problems, name+": "+err.Error())
			continue
		}
		added = append(added, name)
	}
	if len(added) > 0 {
		s.audit(r, "media.upload", strings.Join(added, ", "), "")
	}
	if len(problems) > 0 {
		msg := strings.Join(problems, " ")
		if len(added) > 0 {
			msg = "Uploaded " + strings.Join(added, ", ") + ". " + msg
		}
		s.fail(w, r, http.StatusBadRequest, msg)
		return
	}
	redirect(w, r, "/media", "uploaded")
}

// saveUpload adds a file owned by guildID, "" for shared. Names are unique per owner.
func (s *Server) saveUpload(r *http.Request, src io.Reader, name, guildID string, limit int64) error {
	ctx := r.Context()
	if _, err := s.store.MediaByName(ctx, ownerScope(guildID), name); err == nil {
		return errors.New("a file with this name exists, delete it first or rename the upload")
	}
	tmp, err := s.cfg.MediaDir.Temp()
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(src, limit+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge) || n > limit:
		return fmt.Errorf("larger than %d MB", limit>>20)
	case err != nil:
		return err
	case n == 0:
		return errors.New("the file is empty")
	}
	m, err := media.Probe(ctx, tmp.Name(), name)
	if err != nil {
		return err
	}
	m.UploadedBy, m.GuildID = sessionFrom(r).Username, guildID
	id, err := s.store.AddMedia(ctx, m)
	if errors.Is(err, store.ErrNameTaken) {
		return errors.New("a file with this name exists, delete it first or rename the upload")
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.cfg.MediaDir.Path(id)); err != nil {
		_ = s.store.DeleteMedia(ctx, id)
		return err
	}
	return nil
}

// fetchMedia downloads a file from a link into the media library.
func (s *Server) fetchMedia(w http.ResponseWriter, r *http.Request) {
	owner := r.PostForm.Get("guild")
	if !validOwner(sessionFrom(r), owner) {
		s.notFound(w, r)
		return
	}
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	body, name, err := media.Fetch(r.Context(), r.PostForm.Get("url"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "Not added: "+err.Error()+".")
		return
	}
	defer body.Close()
	if n := strings.TrimSpace(r.PostForm.Get("name")); n != "" {
		name = n
	}
	if name, err = media.CleanName(name); err != nil {
		s.fail(w, r, http.StatusBadRequest, "Not added: "+err.Error()+".")
		return
	}
	if err := s.saveUpload(r, body, name, owner, int64(settings.MaxUploadMB)<<20); err != nil {
		s.fail(w, r, http.StatusBadRequest, name+" not added: "+err.Error())
		return
	}
	s.audit(r, "media.upload", name, r.PostForm.Get("url"))
	redirect(w, r, "/media", "uploaded")
}

// ParseTags accepts "greeting, Funny  sounds" and returns lower case tags without duplicates.
func ParseTags(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		t = strings.ToLower(strings.Join(strings.Fields(t), " "))
		if t != "" && len(t) <= 50 && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out
}

func (s *Server) updateMedia(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMedia(w, r)
	if !ok {
		return
	}
	name, err := media.CleanName(r.PostForm.Get("name"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error()+".")
		return
	}
	tags := ParseTags(r.PostForm.Get("tags"))
	if len(tags) > 20 {
		s.fail(w, r, http.StatusBadRequest, "At most 20 tags.")
		return
	}
	err = s.store.UpdateMedia(r.Context(), m.ID, name, tags)
	if errors.Is(err, store.ErrNameTaken) {
		s.fail(w, r, http.StatusConflict, "A file named "+name+" exists already.")
		return
	}
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.audit(r, "media.update", name, "tags="+strings.Join(tags, ","))
	redirect(w, r, "/media", "saved")
}

func (s *Server) loadMedia(w http.ResponseWriter, r *http.Request) (store.Media, bool) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return store.Media{}, false
	}
	m, err := s.store.Media(r.Context(), id)
	if err == nil && !sessionFrom(r).Scope().Has(m.GuildID) {
		err = store.ErrNotFound
	}
	if err != nil {
		s.storeErr(w, r, err)
		return m, false
	}
	return m, true
}

// ownerScope matches exactly the files of one owner.
func ownerScope(guildID string) store.Scope {
	if guildID == "" {
		return store.GuildScope(true)
	}
	return store.GuildScope(false, guildID)
}

func (s *Server) mediaFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	m, err := s.store.AnyMedia(r.Context(), id)
	if err == nil && !sessionFrom(r).Scope().Has(m.GuildID) {
		err = store.ErrNotFound
	}
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	f, err := os.Open(s.cfg.MediaDir.Path(m.ID))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	disposition := "attachment"
	// SVG can carry scripts, it is only offered as download
	inline := (m.Kind == store.KindImage || m.Kind == store.KindSound || m.Kind == store.KindVideo) && !strings.Contains(m.ContentType, "svg")
	if inline && r.URL.Query().Get("download") == "" {
		disposition = "inline"
		w.Header().Set("Content-Type", m.ContentType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": m.Name}))
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (s *Server) playMedia(w http.ResponseWriter, r *http.Request) {
	if m, ok := s.loadMedia(w, r); ok {
		s.startPlay(w, r, "", r.PostForm.Get("channel"), strconv.FormatInt(m.ID, 10))
	}
}

func (s *Server) deleteMedia(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMedia(w, r)
	if !ok {
		return
	}
	list, err := s.store.ListMedia(r.Context(), sessionFrom(r).Scope(), m.Kind)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, x := range list {
		used := x.UsedBy
		if x.UsedElse > 0 {
			used = append(used, fmt.Sprintf("%d tasks or messages in other servers", x.UsedElse))
		}
		if x.ID == m.ID && len(used) > 0 {
			s.fail(w, r, http.StatusConflict, m.Name+" is used by "+strings.Join(used, ", ")+". Remove it there first.")
			return
		}
	}
	trashed, err := s.removeItem(r, store.TrashMedia, m.ID)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.audit(r, "media.delete", m.Name, trashDetail(trashed))
	removed(w, r, "/media", store.TrashMedia, m.ID, trashed)
}
