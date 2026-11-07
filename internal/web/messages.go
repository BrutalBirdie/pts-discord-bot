package web

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
)

type messageRow struct {
	Message  messages.Message
	Guild    string // name of the server group
	UsedBy   []string
	UsedElse int // tasks in servers the user does not see
}

type messagesData struct {
	Rows   []messageRow
	Groups []bot.Guild
	Group  string // list filter: a server, store.NoGroup or "" for all
	Paging paging
}

func (s *Server) messagesList(w http.ResponseWriter, r *http.Request) {
	d := messagesData{Paging: newPaging(r), Group: r.URL.Query().Get("server")}
	d.Paging.Keep("server", d.Group)
	sc := sessionFrom(r).Scope()
	var err error
	if d.Groups, err = s.groupChoices(r.Context(), r, ""); err != nil {
		s.internalError(w, r, err)
		return
	}
	list, err := s.store.FindMessages(r.Context(), store.MessageQuery{Group: d.Group, Search: d.Paging.Query, Limit: d.Paging.Limit(), Offset: d.Paging.Offset()})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, m := range cut(&d.Paging, list) {
		used, others, err := s.store.MessageUsers(r.Context(), sc, m.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		d.Rows = append(d.Rows, messageRow{Message: m, Guild: s.ownerName(m.GuildID), UsedBy: used, UsedElse: others})
	}
	if d.Group != "" && d.Group != store.NoGroup && !slices.ContainsFunc(d.Groups, func(g bot.Guild) bool { return g.ID == d.Group }) {
		d.Groups = append(d.Groups, bot.Guild{ID: d.Group, Name: s.bot.GuildName(d.Group)})
	}
	s.render(w, r, http.StatusOK, "messages", d)
}

type guildChannels struct {
	Guild bot.Guild
	Text  []bot.Channel
}

type messageEditor struct {
	Message    messages.Message
	New        bool
	Problems   []string
	Unsaved    bool
	SendError  string
	Media      []store.Media
	ByID       map[int64]store.Media
	UsedBy     []string
	UsedElse   int
	Groups     []bot.Guild // servers the message can be grouped with
	Channels   []guildChannels
	Names      map[string]string // user id to name, for mentions in the preview
	FileBytes  int64
	Limits     map[string]int
	Embeddable map[int64]bool
}

func (s *Server) messageEditor(r *http.Request, m messages.Message, isNew bool) (messageEditor, error) {
	e := messageEditor{Message: m, New: isNew, ByID: map[int64]store.Media{}, Names: map[string]string{}, Embeddable: map[int64]bool{},
		Limits: map[string]int{"content": messages.MaxContent, "embeds": messages.MaxEmbeds, "embedTotal": messages.MaxEmbedTotal, "files": messages.MaxFiles}}
	sess := sessionFrom(r)
	var err error
	if e.Groups, err = s.groupChoices(r.Context(), r, m.GuildID); err != nil {
		return e, err
	}
	if e.Media, err = s.messageMedia(r, m); err != nil {
		return e, err
	}
	for _, md := range e.Media {
		e.ByID[md.ID] = md
		e.Embeddable[md.ID] = md.Kind == store.KindImage && embeddable(md.Name)
	}
	for _, id := range attachedIDs(m) {
		e.FileBytes += e.ByID[id].SizeBytes
	}
	if !isNew {
		if e.UsedBy, e.UsedElse, err = s.store.MessageUsers(r.Context(), sess.Scope(), m.ID); err != nil {
			return e, err
		}
	}
	gs := s.guilds(r)
	slices.SortStableFunc(gs, func(a, b bot.Guild) int { return cmpBool(a.ID != m.GuildID, b.ID != m.GuildID) })
	for _, g := range gs {
		gc := guildChannels{Guild: g}
		for _, c := range s.bot.Channels(g.ID) {
			if !c.Voice && c.Usable {
				gc.Text = append(gc.Text, c)
			}
		}
		e.Channels = append(e.Channels, gc)
		if len(messages.MentionRe.FindStringIndex(m.Content)) > 0 {
			for _, mem := range s.bot.Members(g.ID) {
				e.Names[mem.ID] = mem.Name
			}
		}
	}
	return e, nil
}

// messageMedia are the files the editor of m offers: those offered to its
// server group, its own first, and those it attaches.
func (s *Server) messageMedia(r *http.Request, m messages.Message) ([]store.Media, error) {
	list, err := s.store.ListMedia(r.Context(), m.GuildID, "")
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, id := range attachedIDs(m) {
		if !slices.ContainsFunc(list, func(md store.Media) bool { return md.ID == id }) {
			ids = append(ids, id)
		}
	}
	more, err := s.store.MediaByIDs(r.Context(), ids)
	return append(list, more...), err
}

// cmpBool orders false before true.
func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

func embeddable(name string) bool {
	n := strings.ToLower(name)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp"} {
		if strings.HasSuffix(n, ext) {
			return true
		}
	}
	return false
}

// attachedIDs are the files a message uploads: its files and the embed images.
func attachedIDs(m messages.Message) []int64 {
	ids := slices.Clone(m.Files)
	for _, e := range m.Embeds {
		for _, id := range []int64{e.ImageMediaID, e.ThumbMediaID} {
			if id != 0 && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func (s *Server) renderMessage(w http.ResponseWriter, r *http.Request, status int, m messages.Message, isNew bool, problems []string, unsaved bool, sendErr string) {
	e, err := s.messageEditor(r, m, isNew)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	e.Problems, e.Unsaved, e.SendError = problems, unsaved, sendErr
	s.render(w, r, status, "message", e)
}

func (s *Server) newMessage(w http.ResponseWriter, r *http.Request) {
	m := messages.Message{Embeds: []messages.Embed{}}
	if sessionFrom(r).Discord() {
		m.GuildID = s.defaultGuild(r, r.URL.Query().Get("guild"))
	}
	s.renderMessage(w, r, http.StatusOK, m, true, nil, false, "")
}

// ownerName is the name of the server group of a message or file, empty for none.
func (s *Server) ownerName(guildID string) string {
	if guildID == "" {
		return ""
	}
	return s.bot.GuildName(guildID)
}

// otherServers stands in for the names of users the viewer may not see.
func otherServers(n int) string {
	if n == 1 {
		return "1 task in other servers"
	}
	return fmt.Sprintf("%d tasks in other servers", n)
}

func (s *Server) loadMessage(w http.ResponseWriter, r *http.Request) (messages.Message, bool) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return messages.Message{}, false
	}
	m, err := s.store.Message(r.Context(), id)
	if err != nil {
		s.storeErr(w, r, err)
		return m, false
	}
	return m, true
}

func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	if m, ok := s.loadMessage(w, r); ok {
		s.renderMessage(w, r, http.StatusOK, m, false, nil, false, "")
	}
}

func parseMessage(r *http.Request) messages.Message {
	f := r.PostForm
	m := messages.Message{Name: strings.TrimSpace(f.Get("name")), Content: strings.ReplaceAll(f.Get("content"), "\r\n", "\n"), Embeds: []messages.Embed{}, GuildID: f.Get("guild")}
	m.GroupOnly = m.GuildID != "" && f.Get("group_only") == "on"
	n, _ := strconv.Atoi(f.Get("embeds"))
	for i := range min(max(n, 0), messages.MaxEmbeds+1) {
		p := "e" + strconv.Itoa(i) + "."
		e := messages.Embed{
			Title:       strings.TrimSpace(f.Get(p + "title")),
			URL:         strings.TrimSpace(f.Get(p + "url")),
			Description: strings.ReplaceAll(f.Get(p+"desc"), "\r\n", "\n"),
			Color:       f.Get(p + "color"),
			Footer:      strings.TrimSpace(f.Get(p + "footer")),
		}
		e.ImageMediaID, _ = strconv.ParseInt(f.Get(p+"image"), 10, 64)
		e.ThumbMediaID, _ = strconv.ParseInt(f.Get(p+"thumb"), 10, 64)
		nf, _ := strconv.Atoi(f.Get(p + "fields"))
		for j := range min(max(nf, 0), messages.MaxFields+1) {
			q := p + "f" + strconv.Itoa(j) + "."
			e.Fields = append(e.Fields, messages.Field{Name: strings.TrimSpace(f.Get(q + "name")), Value: strings.TrimSpace(f.Get(q + "value")), Inline: f.Get(q+"inline") == "on"})
		}
		m.Embeds = append(m.Embeds, e)
	}
	for _, v := range f["file"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && !slices.Contains(m.Files, id) {
			m.Files = append(m.Files, id)
		}
	}
	return m
}

func applyMessageOp(m *messages.Message, op string, r *http.Request) {
	verb, arg, _ := strings.Cut(op, ":")
	a, b, _ := strings.Cut(arg, ".")
	i, _ := strconv.Atoi(a)
	j, _ := strconv.Atoi(b)
	inEmbeds := i >= 0 && i < len(m.Embeds)
	switch verb {
	case "add-embed":
		if len(m.Embeds) < messages.MaxEmbeds {
			m.Embeds = append(m.Embeds, messages.Embed{Color: "#4752c4"})
		}
	case "del-embed":
		if inEmbeds {
			m.Embeds = slices.Delete(m.Embeds, i, i+1)
		}
	case "add-field":
		if inEmbeds && len(m.Embeds[i].Fields) < messages.MaxFields {
			m.Embeds[i].Fields = append(m.Embeds[i].Fields, messages.Field{})
		}
	case "del-field":
		if inEmbeds && j >= 0 && j < len(m.Embeds[i].Fields) {
			m.Embeds[i].Fields = slices.Delete(m.Embeds[i].Fields, j, j+1)
		}
	case "add-file":
		if id, err := strconv.ParseInt(r.PostForm.Get("file_add"), 10, 64); err == nil && id > 0 && !slices.Contains(m.Files, id) {
			m.Files = append(m.Files, id)
		}
	case "del-file":
		if i >= 0 && i < len(m.Files) {
			m.Files = slices.Delete(m.Files, i, i+1)
		}
	}
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess := sessionFrom(r)
	isNew := r.PathValue("id") == ""
	m := parseMessage(r)
	var old messages.Message
	if !isNew {
		var ok bool
		if old, ok = s.loadMessage(w, r); !ok {
			return
		}
		m.ID = old.ID
	}
	if !validGroup(sess, m.GuildID, old.GuildID) {
		s.fail(w, r, http.StatusBadRequest, "Pick one of your servers.")
		return
	}
	if op := r.PostForm.Get("op"); op != "save" {
		applyMessageOp(&m, op, r)
		s.renderMessage(w, r, http.StatusOK, m, isNew, nil, true, "")
		return
	}
	problems := m.Problems()
	attached, err := s.store.MediaByIDs(ctx, attachedIDs(m))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for _, id := range attachedIDs(m) {
		if !slices.ContainsFunc(attached, func(md store.Media) bool { return md.ID == id }) {
			problems = append(problems, fmt.Sprintf("File %d was deleted from the media library, remove it here.", id))
		}
	}
	if len(problems) > 0 {
		s.renderMessage(w, r, http.StatusUnprocessableEntity, m, isNew, problems, true, "")
		return
	}
	user := sess.Username
	if isNew {
		id, err := s.store.CreateMessage(ctx, m, user)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		m.ID = id
		s.audit(r, "message.create", m.Name, "")
	} else {
		if err := s.store.UpdateMessage(ctx, m, user); err != nil {
			s.storeErr(w, r, err)
			return
		}
		s.audit(r, "message.update", m.Name, "")
	}
	redirect(w, r, fmt.Sprintf("/messages/%d", m.ID), "saved")
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMessage(w, r)
	if !ok {
		return
	}
	channelID := r.PostForm.Get("channel")
	ch, guildID, ok := s.bot.Channel(channelID)
	if !ok || ch.Voice || !sessionFrom(r).CanGuild(guildID) {
		s.renderMessage(w, r, http.StatusBadRequest, m, false, nil, false, "Pick a text channel.")
		return
	}
	if err := s.run.SendNow(guildID, channelID, m.ID, sessionFrom(r).Username, "Send "+m.Name); err != nil {
		s.renderMessage(w, r, http.StatusBadGateway, m, false, nil, false, "Not sent: "+err.Error())
		return
	}
	s.audit(r, "message.send", m.Name, s.bot.GuildName(guildID)+" · #"+ch.Name)
	redirect(w, r, fmt.Sprintf("/messages/%d", m.ID), "sent")
}

func (s *Server) duplicateMessage(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMessage(w, r)
	if !ok {
		return
	}
	m.Name += " (copy)"
	id, err := s.store.CreateMessage(r.Context(), m, sessionFrom(r).Username)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r, "message.create", m.Name, "copy")
	redirect(w, r, fmt.Sprintf("/messages/%d", id), "copied")
}

func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMessage(w, r)
	if !ok {
		return
	}
	used, others, err := s.store.MessageUsers(r.Context(), sessionFrom(r).Scope(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if others > 0 {
		used = append(used, otherServers(others))
	}
	if len(used) > 0 {
		s.fail(w, r, http.StatusConflict, "The message is used by the tasks "+strings.Join(used, ", ")+". Remove it from their steps first.")
		return
	}
	trashed, err := s.removeItem(r, store.TrashMessage, m.ID)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.audit(r, "message.delete", m.Name, trashDetail(trashed))
	removed(w, r, "/messages", store.TrashMessage, m.ID, trashed)
}
