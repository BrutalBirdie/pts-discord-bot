package web

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/media"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
)

type voiceGroup struct {
	Guild bot.Guild
	Voice []bot.Channel
}

type mediaData struct {
	Items        []store.Media
	Kind         string
	Tag          string
	Tags         []string
	Counts       map[string]int
	TotalBytes   int64
	Voice        []voiceGroup
	MaxUploadMB  int
	Groups       []bot.Guild // servers a file can be grouped with
	Group        string      // list filter: a server, store.NoGroup or "" for all
	DefaultGroup string      // preselected for uploads: the server of a Discord user
	Names        map[string]string
	Paging       paging
	Edit         *store.Media // the sound in the Edit sound sheet
	Emojis       []bot.Emoji
	Images       []store.Media
	Self         string
}

// GroupsWith are the group choices plus guildID, a file's current group.
func (d mediaData) GroupsWith(guildID string) []bot.Guild {
	if guildID == "" || slices.ContainsFunc(d.Groups, func(g bot.Guild) bool { return g.ID == guildID }) {
		return d.Groups
	}
	return append(slices.Clone(d.Groups), bot.Guild{ID: guildID, Name: groupLabel(d.Names, guildID)})
}

// GroupName is the name of the server group of a file, empty for none.
func (d mediaData) GroupName(m store.Media) string {
	return groupLabel(d.Names, m.GuildID)
}

func groupLabel(names map[string]string, guildID string) string {
	if guildID == "" {
		return ""
	}
	if n := names[guildID]; n != "" {
		return n
	}
	return guildID
}

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
	sum, err := s.store.MediaSummary(ctx)
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
	d := mediaData{Kind: q.Get("kind"), Tag: q.Get("tag"), Group: q.Get("server"), Counts: sum.Counts, TotalBytes: sum.Bytes, Tags: sum.Tags, Voice: s.voiceGroups(r), MaxUploadMB: settings.MaxUploadMB, Paging: newPaging(r),
		Names: map[string]string{}}
	if d.Groups, err = s.groupChoices(ctx, r, ""); err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, g := range d.Groups {
		d.Names[g.ID] = g.Name
	}
	if sess.Discord() && len(d.Groups) > 0 {
		d.DefaultGroup = d.Groups[0].ID
	}
	d.Paging.Keep("kind", d.Kind)
	d.Paging.Keep("tag", d.Tag)
	d.Paging.Keep("server", d.Group)
	items, err := s.store.FindMedia(ctx, store.MediaQuery{Scope: sess.Scope(), Group: d.Group, Kind: d.Kind, Tag: d.Tag, Search: d.Paging.Query, Limit: d.Paging.Limit(), Offset: d.Paging.Offset()})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	d.Items = cut(&d.Paging, items)
	d.Self = d.Paging.Self()
	if id, err := strconv.ParseInt(q.Get("edit"), 10, 64); err == nil {
		if m, err := s.store.Media(ctx, id); err == nil && m.Kind == store.KindSound {
			d.Edit = &m
			guild := m.GuildID
			if guild == "" && len(d.Groups) > 0 {
				guild = d.Groups[0].ID
			}
			d.Emojis = s.bot.Emojis(guild)
			if d.Images, err = s.store.ListMedia(ctx, guild, store.KindImage); err != nil {
				s.internalError(w, r, err)
				return
			}
		}
	}
	for _, m := range d.Items {
		if _, ok := d.Names[m.GuildID]; m.GuildID != "" && !ok {
			d.Names[m.GuildID] = s.bot.GuildName(m.GuildID)
		}
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
	group, only, next := "", false, ""
	var tags []string
	var added, problems []string
	var saved []store.Media
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
		if part.FormName() == "tag" {
			v, _ := io.ReadAll(io.LimitReader(part, 64))
			tags = ParseTags(string(v))
			continue
		}
		if part.FormName() == "next" {
			v, _ := io.ReadAll(io.LimitReader(part, 512))
			next = safeNext(string(v), "")
			continue
		}
		if part.FormName() == "guild" {
			v, _ := io.ReadAll(io.LimitReader(part, 64))
			group = string(v)
			continue
		}
		if part.FormName() == "group_only" {
			v, _ := io.ReadAll(io.LimitReader(part, 8))
			only = string(v) == "on"
			continue
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		if !csrfOK {
			s.fail(w, r, http.StatusForbidden, "The request could not be verified. Reload the page and try again.")
			return
		}
		if !validGroup(sess, group, "") {
			s.fail(w, r, http.StatusBadRequest, "Pick one of your servers.")
			return
		}
		name, err := media.CleanName(part.FileName())
		if err != nil {
			problems = append(problems, part.FileName()+": "+err.Error()+".")
			continue
		}
		m, err := s.saveUpload(r, part, store.Media{Name: name, GuildID: group, GroupOnly: only}, limit)
		if err != nil {
			problems = append(problems, name+": "+err.Error())
			continue
		}
		if len(tags) > 0 {
			m.Tags = tags
			if err := s.store.UpdateMedia(r.Context(), m); err != nil {
				s.log.Error("tagging an upload", "err", err)
			}
		}
		added = append(added, name)
		saved = append(saved, m)
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
	// a single new sound opens Edit sound, to cut it right away
	if len(saved) == 1 && saved[0].Kind == store.KindSound {
		back := "/media?"
		if next != "" {
			back = next + map[bool]string{true: "&", false: "?"}[strings.Contains(next, "?")]
		}
		http.Redirect(w, r, fmt.Sprintf("%sedit=%d&done=uploaded#sheet-sound", back, saved[0].ID), http.StatusSeeOther)
		return
	}
	redirect(w, r, cmp.Or(next, "/media"), "uploaded")
}

// saveUpload adds a file with the name and server group of want. Names are unique.
func (s *Server) saveUpload(r *http.Request, src io.Reader, want store.Media, limit int64) (store.Media, error) {
	ctx := r.Context()
	name := want.Name
	if _, err := s.store.MediaByName(ctx, name); err == nil {
		return store.Media{}, errors.New("a file with this name exists, delete it first or rename the upload")
	}
	tmp, err := s.cfg.MediaDir.Temp()
	if err != nil {
		return store.Media{}, err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(src, limit+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge) || n > limit:
		return store.Media{}, fmt.Errorf("larger than %d MB", limit>>20)
	case err != nil:
		return store.Media{}, err
	case n == 0:
		return store.Media{}, errors.New("the file is empty")
	}
	m, err := media.Probe(ctx, tmp.Name(), name)
	if err != nil {
		return store.Media{}, err
	}
	m.UploadedBy, m.GuildID, m.GroupOnly = sessionFrom(r).Username, want.GuildID, want.GroupOnly
	id, err := s.store.AddMedia(ctx, m)
	if errors.Is(err, store.ErrNameTaken) {
		return store.Media{}, errors.New("a file with this name exists, delete it first or rename the upload")
	}
	if err != nil {
		return store.Media{}, err
	}
	if err := os.Rename(tmp.Name(), s.cfg.MediaDir.Path(id)); err != nil {
		_ = s.store.DeleteMedia(ctx, id)
		return store.Media{}, err
	}
	m.ID = id
	return m, nil
}

// fetchMedia downloads a file from a link into the media library.
func (s *Server) fetchMedia(w http.ResponseWriter, r *http.Request) {
	group := r.PostForm.Get("guild")
	if !validGroup(sessionFrom(r), group, "") {
		s.fail(w, r, http.StatusBadRequest, "Pick one of your servers.")
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
	if _, err := s.saveUpload(r, body, store.Media{Name: name, GuildID: group, GroupOnly: r.PostForm.Get("group_only") == "on"}, int64(settings.MaxUploadMB)<<20); err != nil {
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

const maxTags = 20

// tagTokens is the tag picker of a file.
func tagTokens(m store.Media) tokenField {
	f := tokenField{Field: "tag", Find: "tag_find", Label: "Tags", LabelID: fmt.Sprintf("tags-%d", m.ID), Placeholder: "Search or add tags", Empty: "No tags",
		Help: "A Play step or /play can pick a random sound with a tag.", Source: "/tags", Max: maxTags, Plain: true}
	for _, t := range m.Tags {
		f.Items = append(f.Items, token{Value: t, Name: t, Title: t, Icon: "hash"})
	}
	return f
}

// searchTags feeds the tag pickers: the tags in use that contain q, and q itself as a new tag.
func (s *Server) searchTags(w http.ResponseWriter, r *http.Request) {
	sum, err := s.store.MediaSummary(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	type hit struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Tag  bool   `json:"tag"`
		New  bool   `json:"new,omitempty"`
	}
	q := ParseTags(r.URL.Query().Get("q"))
	hits := []hit{}
	for _, t := range sum.Tags {
		if (len(q) == 0 || strings.Contains(t, q[0])) && len(hits) < 14 {
			hits = append(hits, hit{ID: t, Name: t, Tag: true})
		}
	}
	if len(q) == 1 && !slices.Contains(sum.Tags, q[0]) {
		hits = append(hits, hit{ID: q[0], Name: q[0], Tag: true, New: true})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(hits)
}

// maxDisplayName fits a soundboard tile and Discord's 100 characters of an autocomplete choice.
const maxDisplayName = 80

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
	tags := ParseTags(strings.Join(append(r.PostForm["tag"], r.PostForm.Get("tags"), r.PostForm.Get("tag_find")), ","))
	if len(tags) > maxTags {
		s.fail(w, r, http.StatusBadRequest, "At most 20 tags.")
		return
	}
	group := r.PostForm.Get("guild")
	if !validGroup(sessionFrom(r), group, m.GuildID) {
		s.fail(w, r, http.StatusBadRequest, "Pick one of your servers.")
		return
	}
	only := r.PostForm.Get("group_only") == "on" && group != ""
	display := strings.TrimSpace(r.PostForm.Get("display_name"))
	if utf8.RuneCountInString(display) > maxDisplayName {
		s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("The display name is longer than %d characters.", maxDisplayName))
		return
	}
	err = s.store.UpdateMedia(r.Context(), store.Media{ID: m.ID, Name: name, DisplayName: display, Tags: tags, GuildID: group, GroupOnly: only})
	if errors.Is(err, store.ErrNameTaken) {
		s.fail(w, r, http.StatusConflict, "A file named "+name+" exists already.")
		return
	}
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.audit(r, "media.update", name, "tags="+strings.Join(tags, ",")+", server group "+groupDetail(s.groupName(group), only))
	redirect(w, r, "/media", "saved")
}

func (s *Server) loadMedia(w http.ResponseWriter, r *http.Request) (store.Media, bool) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return store.Media{}, false
	}
	m, err := s.store.Media(r.Context(), id)
	if err != nil {
		s.storeErr(w, r, err)
		return m, false
	}
	return m, true
}

func (s *Server) mediaFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	m, err := s.store.AnyMedia(r.Context(), id)
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
	list, err := s.store.FindMedia(r.Context(), store.MediaQuery{Scope: sessionFrom(r).Scope(), Kind: m.Kind})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, x := range list {
		used := x.UsedBy
		if x.UsedElse > 0 {
			used = append(used, otherServers(x.UsedElse))
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
