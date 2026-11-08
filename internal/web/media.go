package web

import (
	"archive/zip"
	"cmp"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/media"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

type voiceGroup struct {
	Guild bot.Guild
	Voice []bot.Channel
}

type mediaData struct {
	Guild        string // the open server, its files come first
	Base         string // the media page of the open server
	GuildName    string
	Split        int // the first item of another server, -1 when all are of the open one
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
	Edit         *store.Media // the file in the Edit sheet, sounds get Edit sound
	Emojis       []bot.Emoji
	Images       []store.Media
	Self         string
	Batch        string        // the files of one upload
	Uploaded     []store.Media // all of them
	Problems     []string      // files of the upload that were not added
	Review       *review       // Edit goes through the files of the upload
	Ask          bool          // just uploaded or tagged: the Uploaded sheet opens and asks what next
	Tagged       bool
}

// BatchTags is the tag picker for all files of the upload, in the sheet or on the page.
func (d mediaData) BatchTags(where string) tokenField {
	return tokenField{Field: "tag", Find: "tag_find", Label: "Tags for all of them", LabelID: "tags-batch-" + where, Placeholder: "Search or add tags", Empty: "No tags picked",
		Help: "Added to the tags each file has.", Source: "/tags", Max: maxTags, Plain: true}
}

// BulkTags is the tag picker for the files selected in the list.
func (d mediaData) BulkTags() tokenField {
	return tokenField{Field: "tag", Find: "tag_find", Label: "Tags to add to the selected files", LabelID: "tags-bulk", Placeholder: "Search or add tags", Empty: "No tags picked",
		Source: "/tags", Max: maxTags, Plain: true}
}

// review is the place of the file in the Edit sheet among the files of an upload.
type review struct {
	Pos, Total int
	Next       string // the next file's sheet, or the upload's list after the last
	Last       bool
}

// EditNext is where the Edit sheet goes after saving.
func (d mediaData) EditNext() string {
	if d.Review != nil {
		return d.Review.Next
	}
	return d.Self
}

// UploadedKinds counts the files of the upload by kind, like "3 sounds, 1 image".
func (d mediaData) UploadedKinds() string {
	n := map[string]int{}
	for _, m := range d.Uploaded {
		n[m.Kind]++
	}
	var out []string
	for _, k := range []struct{ kind, one, many string }{{store.KindSound, "sound", "sounds"}, {store.KindImage, "image", "images"}, {store.KindVideo, "video", "videos"}, {store.KindFile, "other file", "other files"}} {
		switch n[k.kind] {
		case 0:
		case 1:
			out = append(out, "1 "+k.one)
		default:
			out = append(out, fmt.Sprintf("%d %s", n[k.kind], k.many))
		}
	}
	return strings.Join(out, ", ")
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

// mediaPage shows the media library of a server. /media opens the server of
// ?server, else the user's first, and lists all files when there is none.
func (s *Server) mediaPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		if g, ok := s.serverOf(r); ok {
			q := r.URL.Query()
			q.Del("server")
			http.Redirect(w, r, withQuery("/room/"+g.ID+"/media", q.Encode()), http.StatusSeeOther)
			return
		}
	} else if _, ok := s.bot.Guild(id); !ok || !sessionFrom(r).CanGuild(id) {
		s.notFound(w, r)
		return
	}
	s.showMedia(w, r, id, r.URL.Query(), nil)
}

// mediaBase is the media page of a server, or /media without one.
func mediaBase(guildID string) string {
	if guildID == "" {
		return "/media"
	}
	return "/room/" + guildID + "/media"
}

// showMedia shows the media library, the files of the server first, with
// batch only the files of that upload and the problems of the files it could not add.
func (s *Server) showMedia(w http.ResponseWriter, r *http.Request, guildID string, q url.Values, problems []string) {
	ctx := r.Context()
	if guildID != "" {
		r = at(r, place{Guild: guildID, Section: "media", Members: true})
	}
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
	d := mediaData{Guild: guildID, Base: mediaBase(guildID), Kind: q.Get("kind"), Tag: q.Get("tag"), Group: q.Get("server"), Counts: sum.Counts, TotalBytes: sum.Bytes, Tags: sum.Tags, Voice: s.voiceGroups(r), MaxUploadMB: settings.MaxUploadMB, Paging: newPaging(r),
		Names: map[string]string{}, Batch: q.Get("batch"), Problems: problems}
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
	if guildID != "" {
		d.DefaultGroup, d.Group = guildID, ""
	}
	d.Paging.Keep("kind", d.Kind)
	d.Paging.Keep("tag", d.Tag)
	d.Paging.Keep("server", d.Group)
	d.Paging.Keep("batch", d.Batch)
	if d.Batch != "" {
		if d.Uploaded, err = s.store.FindMedia(ctx, store.MediaQuery{Scope: sess.Scope(), Batch: d.Batch}); err != nil {
			s.internalError(w, r, err)
			return
		}
		done := q.Get("done")
		d.Tagged = done == "tagged"
		d.Ask = q.Get("edit") == "" && (len(problems) > 0 || len(d.Uploaded) > 1 && (done == "uploaded" || d.Tagged))
	}
	items, err := s.store.FindMedia(ctx, store.MediaQuery{Scope: sess.Scope(), Offer: guildID, Group: d.Group, Kind: d.Kind, Tag: d.Tag, Batch: d.Batch, Search: d.Paging.Query, Limit: d.Paging.Limit(), Offset: d.Paging.Offset()})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	d.Items = cut(&d.Paging, items)
	d.Self = d.Paging.Self()
	d.Split = -1
	if guildID != "" {
		d.Split = slices.IndexFunc(d.Items, func(m store.Media) bool { return m.GuildID != guildID })
		d.GuildName = s.bot.GuildName(guildID)
	}
	if id, err := strconv.ParseInt(q.Get("edit"), 10, 64); err == nil {
		if m, err := s.store.Media(ctx, id); err == nil {
			d.Edit = &m
			d.Review = d.reviewOf(m.ID)
		}
		if m := d.Edit; m != nil && m.Kind == store.KindSound {
			guild := m.GuildID
			if guild == "" {
				guild = guildID
			}
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

// reviewOf places a file among the files of the upload, nil when it is not one of them.
func (d mediaData) reviewOf(id int64) *review {
	i := slices.IndexFunc(d.Uploaded, func(m store.Media) bool { return m.ID == id })
	if i < 0 || len(d.Uploaded) < 2 {
		return nil
	}
	rv := &review{Pos: i + 1, Total: len(d.Uploaded), Next: d.Base + "?batch=" + url.QueryEscape(d.Batch), Last: i == len(d.Uploaded)-1}
	if !rv.Last {
		rv.Next += "&edit=" + strconv.FormatInt(d.Uploaded[i+1].ID, 10)
	}
	return rv
}

// upload streams the files of a multipart form into the media directory and
// unpacks zip archives. The csrf field must come before the files.
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
	want := store.Media{Batch: uploadBatch()}
	next := ""
	var tags []string
	var problems []string
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
		switch part.FormName() {
		case "csrf":
			token, _ := io.ReadAll(io.LimitReader(part, 256))
			csrfOK = s.validCSRF(r, sess, string(token))
			continue
		case "tag":
			v, _ := io.ReadAll(io.LimitReader(part, 64))
			tags = ParseTags(strings.Join(append(tags, string(v)), ","))
			continue
		case "next":
			v, _ := io.ReadAll(io.LimitReader(part, 512))
			next = safeNext(string(v), "")
			continue
		case "guild":
			v, _ := io.ReadAll(io.LimitReader(part, 64))
			want.GuildID = string(v)
			continue
		case "group_only":
			v, _ := io.ReadAll(io.LimitReader(part, 8))
			want.GroupOnly = string(v) == "on"
			continue
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		if !csrfOK {
			s.fail(w, r, http.StatusForbidden, "The request could not be verified. Reload the page and try again.")
			return
		}
		if !validGroup(sess, want.GuildID, "") {
			s.fail(w, r, http.StatusBadRequest, "Pick one of your servers.")
			return
		}
		name, err := media.CleanName(part.FileName())
		if err != nil {
			problems = append(problems, part.FileName()+": "+err.Error()+".")
			continue
		}
		if strings.EqualFold(filepath.Ext(name), ".zip") {
			got, bad := s.unpackZip(r, part, name, want, limit)
			saved, problems = append(saved, got...), append(problems, bad...)
			continue
		}
		want.Name = name
		m, err := s.saveUpload(r, part, want, limit)
		if err != nil {
			problems = append(problems, name+": "+err.Error()+".")
			continue
		}
		saved = append(saved, m)
	}
	var added []string
	ids := make([]int64, 0, len(saved))
	for i := range saved {
		added = append(added, saved[i].Name)
		ids = append(ids, saved[i].ID)
		if len(tags) > 0 {
			saved[i].Tags = tags
		}
	}
	if len(tags) > 0 && len(ids) > 0 {
		if err := s.store.AddTags(ctx, ids, tags); err != nil {
			s.log.Error("tagging an upload", "err", err)
		}
	}
	if len(added) > 0 {
		s.audit(r, "media.upload", strings.Join(added, ", "), "")
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		type file struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Kind       string `json:"kind"`
			DurationMS int    `json:"duration_ms"`
		}
		out := struct {
			Added    []file   `json:"added"`
			Problems []string `json:"problems"`
		}{Added: []file{}, Problems: problems}
		for _, m := range saved {
			out.Added = append(out.Added, file{ID: m.ID, Name: m.Label(), Kind: m.Kind, DurationMS: m.PlayMS()})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	if len(saved) == 0 {
		s.fail(w, r, http.StatusBadRequest, cmp.Or(strings.Join(problems, " "), "Pick the files to upload first."))
		return
	}
	if next != "" {
		if len(problems) > 0 {
			s.fail(w, r, http.StatusBadRequest, "Uploaded "+strings.Join(added, ", ")+". "+strings.Join(problems, " "))
			return
		}
		// a single new sound opens Edit sound, to cut it right away
		if len(saved) == 1 && saved[0].Kind == store.KindSound {
			sep := map[bool]string{true: "&", false: "?"}[strings.Contains(next, "?")]
			http.Redirect(w, r, fmt.Sprintf("%s%sedit=%d&done=uploaded#sheet-edit", next, sep, saved[0].ID), http.StatusSeeOther)
			return
		}
		redirect(w, r, next, "uploaded")
		return
	}
	// the files just uploaded, to tag them all at once or review them one by one
	if len(problems) > 0 {
		s.showMedia(w, r, want.GuildID, url.Values{"batch": {want.Batch}}, problems)
		return
	}
	target := mediaBase(want.GuildID) + "?batch=" + want.Batch
	if len(saved) == 1 {
		target += "&edit=" + strconv.FormatInt(saved[0].ID, 10)
	}
	redirect(w, r, target, "uploaded")
}

func uploadBatch() string {
	b := make([]byte, 9)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

const maxZipFiles = 500

// unpackZip adds the files of a zip archive, without its folders. All files
// together may be four times the upload limit, each one the limit.
func (s *Server) unpackZip(r *http.Request, src io.Reader, name string, want store.Media, limit int64) (saved []store.Media, problems []string) {
	tmp, err := s.cfg.MediaDir.Temp()
	if err != nil {
		return nil, []string{name + ": " + err.Error() + "."}
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(src, limit+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge) || n > limit:
		return nil, []string{fmt.Sprintf("%s: larger than %d MB.", name, limit>>20)}
	case err != nil:
		return nil, []string{name + ": " + err.Error() + "."}
	}
	zr, err := zip.OpenReader(tmp.Name())
	// the names are cut to the file name, a path outside the archive does no harm
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, []string{name + ": not a zip archive the bot can read."}
	}
	defer zr.Close()
	var total int64
	count := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		full := strings.ReplaceAll(f.Name, `\`, "/")
		base := path.Base(full)
		// macOS adds __MACOSX/ and .DS_Store to archives
		if strings.HasPrefix(full, "__MACOSX/") || strings.HasPrefix(base, ".") {
			continue
		}
		if strings.EqualFold(path.Ext(base), ".zip") {
			problems = append(problems, base+": archives inside "+name+" are not unpacked.")
			continue
		}
		if count++; count > maxZipFiles {
			problems = append(problems, fmt.Sprintf("Only the first %d files of %s were unpacked.", maxZipFiles, name))
			break
		}
		if total > 4*limit {
			problems = append(problems, fmt.Sprintf("%s unpacks to more than %d MB, the rest was not unpacked.", name, 4*limit>>20))
			break
		}
		clean, err := media.CleanName(base)
		if err != nil {
			problems = append(problems, base+": "+err.Error()+".")
			continue
		}
		rc, err := f.Open()
		if err != nil {
			problems = append(problems, clean+": "+err.Error()+".")
			continue
		}
		want.Name = clean
		m, err := s.saveUpload(r, rc, want, limit)
		rc.Close()
		if err != nil {
			problems = append(problems, clean+": "+err.Error()+".")
			continue
		}
		total += m.SizeBytes
		saved = append(saved, m)
	}
	if len(saved) == 0 && len(problems) == 0 {
		problems = append(problems, name+": the archive has no files.")
	}
	return saved, problems
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
	m.UploadedBy, m.GuildID, m.GroupOnly, m.Batch = sessionFrom(r).Username, want.GuildID, want.GroupOnly, want.Batch
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
	redirect(w, r, mediaBase(group), "uploaded")
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

// updateMedia saves the Edit sheet of a file. Fields the form does not send
// keep their value: the tags without the tag field, the server group without
// its picker, and the sound settings for files that are no sound.
func (s *Server) updateMedia(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMedia(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	f := r.PostForm
	if name := strings.TrimSpace(f.Get("name")); name != "" && name != m.Name {
		clean, err := media.CleanName(name)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error()+".")
			return
		}
		m.Name = clean
	}
	if f.Has("display_name") {
		m.DisplayName = strings.TrimSpace(f.Get("display_name"))
		if utf8.RuneCountInString(m.DisplayName) > maxDisplayName {
			s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("The display name is longer than %d characters.", maxDisplayName))
			return
		}
	}
	if f.Has("tag_find") || f.Has("tags") || f.Has("tag") {
		m.Tags = ParseTags(strings.Join(append(f["tag"], f.Get("tags"), f.Get("tag_find")), ","))
		if len(m.Tags) > maxTags {
			s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("At most %d tags.", maxTags))
			return
		}
	}
	if f.Has("guild") {
		group := f.Get("guild")
		if !validGroup(sessionFrom(r), group, m.GuildID) {
			s.fail(w, r, http.StatusBadRequest, "Pick one of your servers.")
			return
		}
		m.GuildID, m.GroupOnly = group, f.Get("group_only") == "on" && group != ""
	}
	if (f.Has("trim_start") || f.Has("emoji") || f.Has("icon") || f.Has("own_volume")) && m.Kind == store.KindSound {
		m.Emoji, m.IconID, m.Volume = strings.TrimSpace(f.Get("emoji")), 0, 0
		if !validEmoji(m.Emoji) {
			s.fail(w, r, http.StatusBadRequest, "Use one emoji.")
			return
		}
		if v := f.Get("icon"); v != "" && v != "0" {
			m.IconID, _ = strconv.ParseInt(v, 10, 64)
			img, err := s.store.Media(ctx, m.IconID)
			if err != nil || img.Kind != store.KindImage {
				s.fail(w, r, http.StatusBadRequest, "Pick an image from the media library.")
				return
			}
			m.Emoji = ""
		}
		if f.Get("own_volume") == "on" {
			v, err := strconv.Atoi(f.Get("volume"))
			if err != nil || v < 1 || v > tasks.MaxVolume {
				s.fail(w, r, http.StatusBadRequest, "The volume must be between 1 and 200 %.")
				return
			}
			m.Volume = v
		}
		m.TrimStartMS, m.TrimEndMS = millis(f.Get("trim_start")), millis(f.Get("trim_end"))
		if m.DurationMS > 0 && m.TrimEndMS >= m.DurationMS {
			m.TrimEndMS = 0
		}
		if m.TrimStartMS < 0 || m.TrimEndMS < 0 || m.TrimEndMS > 0 && m.TrimEndMS <= m.TrimStartMS || m.DurationMS > 0 && m.TrimStartMS >= m.DurationMS {
			s.fail(w, r, http.StatusBadRequest, "The start of the sound must be before its end.")
			return
		}
	}
	err := s.store.UpdateMedia(ctx, m)
	if errors.Is(err, store.ErrNameTaken) {
		s.fail(w, r, http.StatusConflict, "A file named "+m.Name+" exists already.")
		return
	}
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.audit(r, "media.update", m.Name, "tags="+strings.Join(m.Tags, ",")+", server group "+groupDetail(s.groupName(m.GuildID), m.GroupOnly))
	redirect(w, r, safeNext(f.Get("next"), "/media"), "saved")
}

// tagMedia adds tags to the files selected on the media page, or to all files of an upload.
func (s *Server) tagMedia(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tags := ParseTags(strings.Join(append(r.PostForm["tag"], r.PostForm.Get("tag_find"), r.PostForm.Get("tags")), ","))
	if len(tags) == 0 {
		s.fail(w, r, http.StatusBadRequest, "Pick the tags to add first.")
		return
	}
	ids := formIDs(r.PostForm["id"])
	if batch := r.PostForm.Get("batch"); batch != "" {
		list, err := s.store.FindMedia(ctx, store.MediaQuery{Scope: sessionFrom(r).Scope(), Batch: batch})
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		for _, m := range list {
			if !slices.Contains(ids, m.ID) {
				ids = append(ids, m.ID)
			}
		}
	}
	if len(ids) == 0 {
		s.fail(w, r, http.StatusBadRequest, "Select the files first.")
		return
	}
	list, err := s.store.MediaByIDs(ctx, ids)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if len(list) != len(ids) {
		s.notFound(w, r)
		return
	}
	names := make([]string, 0, len(list))
	for _, m := range list {
		if n := len(ParseTags(strings.Join(append(m.Tags, tags...), ","))); n > maxTags {
			s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("%s would have %d tags, at most %d fit.", m.Name, n, maxTags))
			return
		}
		names = append(names, m.Name)
	}
	if err := s.store.AddTags(ctx, ids, tags); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r, "media.tag", strings.Join(names, ", "), "added tags="+strings.Join(tags, ","))
	redirect(w, r, safeNext(r.PostForm.Get("next"), "/media"), "tagged")
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
