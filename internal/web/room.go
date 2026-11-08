package web

import (
	"cmp"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

const (
	soundsPerPage  = 48
	membersPerPage = 20
	maxEmojiRunes  = 10
	// mentions of other members show their ID in the preview
	maxPreviewNames = 2000
)

type roomChannel struct {
	bot.Channel
	Tasks   int
	Current bool
}

type soundTile struct {
	store.Media
	Fav bool
}

type roomData struct {
	Guild   bot.Guild
	View    string // voice, text, member or "" for the overview
	Channel bot.Channel
	Member  bot.Member
	Self    string // this page, for the next field of forms

	Playing []playingView

	Favorites []soundTile
	Own       []soundTile // sounds of this server's group
	Rest      []soundTile
	Paging    paging
	Tags      []string         // the soundboard shows only the sounds with all of these tags
	SoundTags []store.SoundTag // the tags of the sounds offered to the server
	TagEdit   *store.SoundTag  // the tag in the tag icon sheet
	Edit      *store.Media     // the sound in the Edit sound sheet
	Emojis    []bot.Emoji      // custom emojis of the server, for the emoji picker
	Images    []store.Media
	Groups    []bot.Guild // server group choices of the sound in the Edit sheet

	Messages []messages.Message
	Author   bot.Member        // the bot in this server, for the message preview
	Names    map[string]string // member names for mentions in the preview
	Tasks    []taskRow         // of the channel, or for the member
	Except   []taskRow         // that leave the member out
	Greets   []tasks.Task      // event tasks that start when the member joins voice
}

// tagsURL is the soundboard of the channel with only the sounds that have all of the tags.
func (d roomData) tagsURL(tags []string) string {
	v := url.Values{"c": {d.Channel.ID}}
	for _, t := range tags {
		v.Add("tag", t)
	}
	return "/room/" + d.Guild.ID + "?" + v.Encode()
}

// AllURL shows every sound, HereURL the soundboard with the picked tags, OnlyURL the sounds of one tag.
func (d roomData) AllURL() string            { return d.tagsURL(nil) }
func (d roomData) HereURL() string           { return d.tagsURL(d.Tags) }
func (d roomData) OnlyURL(tag string) string { return d.tagsURL([]string{tag}) }

// ToggleURL adds the tag to the picked tags, or takes it away.
func (d roomData) ToggleURL(tag string) string {
	if d.TagOn(tag) {
		return d.tagsURL(slices.DeleteFunc(slices.Clone(d.Tags), func(t string) bool { return t == tag }))
	}
	return d.tagsURL(append(slices.Clone(d.Tags), tag))
}

func (d roomData) TagOn(tag string) bool { return slices.Contains(d.Tags, tag) }

// TagEditURL opens the icon sheet of a tag.
func (d roomData) TagEditURL(tag string) string {
	return d.HereURL() + "&" + url.Values{"tagedit": {tag}}.Encode()
}

func (d roomData) URL(key, value string) string {
	v := url.Values{}
	if key != "" {
		v.Set(key, value)
	}
	if len(v) == 0 {
		return "/room/" + d.Guild.ID
	}
	return "/room/" + d.Guild.ID + "?" + v.Encode()
}

func (s *Server) roomHome(w http.ResponseWriter, r *http.Request) {
	gs := s.guilds(r)
	if len(gs) == 0 {
		s.render(w, r, http.StatusOK, "room", roomData{})
		return
	}
	http.Redirect(w, r, "/room/"+gs[0].ID, http.StatusSeeOther)
}

func (s *Server) room(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess := sessionFrom(r)
	g, ok := s.bot.Guild(r.PathValue("id"))
	if !ok || !sess.CanGuild(g.ID) {
		s.notFound(w, r)
		return
	}
	r = at(r, place{Guild: g.ID, Members: true})
	q := r.URL.Query()
	q.Del("done")
	d := roomData{Guild: g, Self: r.URL.Path, Paging: newPaging(r)}
	if len(q) > 0 {
		d.Self += "?" + q.Encode()
	}
	d.Paging.Per = soundsPerPage
	for _, p := range s.playingViews(r) {
		if p.P.GuildID == g.ID {
			p.Next = d.Self
			d.Playing = append(d.Playing, p)
		}
	}
	all, err := s.store.ListTasks(ctx, store.GuildScope(g.ID))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	all = slices.DeleteFunc(all, func(t tasks.Task) bool { return t.GuildID != g.ID })
	var mine, left []tasks.Task

	if c := q.Get("c"); c != "" {
		ch, chGuild, ok := s.bot.Channel(c)
		if ok && chGuild == g.ID {
			d.Channel, d.View = ch, "text"
			if ch.Voice {
				d.View = "voice"
			}
		}
	}
	members := s.bot.Members(g.ID)
	if m := q.Get("m"); m != "" {
		if i := slices.IndexFunc(members, func(x bot.Member) bool { return x.ID == m }); i >= 0 {
			d.Member, d.View = members[i], "member"
		}
	}
	switch d.View {
	case "voice":
		if err := s.roomSounds(r, &d); err != nil {
			s.internalError(w, r, err)
			return
		}
		fallthrough
	case "text":
		for _, t := range all {
			if usesChannel(t, d.Channel.ID) {
				mine = append(mine, t)
			}
		}
		if d.View == "text" {
			if d.Messages, err = s.store.ListMessages(ctx, g.ID); err != nil {
				s.internalError(w, r, err)
				return
			}
		}
	case "member":
		for _, t := range all {
			only, except := memberTasks(t, d.Member.ID)
			if only {
				mine = append(mine, t)
			}
			if except {
				left = append(left, t)
			}
			if t.Enabled && t.Trigger.Kind == tasks.Event && t.Trigger.Event == tasks.VoiceJoin && startsFor(t.Trigger, d.Member.ID) {
				d.Greets = append(d.Greets, t)
			}
		}
	}
	if d.Tasks, err = s.taskRows(r, mine, false); err != nil {
		s.internalError(w, r, err)
		return
	}
	if d.Except, err = s.taskRows(r, left, false); err != nil {
		s.internalError(w, r, err)
		return
	}
	if d.View == "text" || d.View == "member" {
		d.Author, d.Names = s.author(g.ID), map[string]string{}
		for _, m := range members[:min(len(members), maxPreviewNames)] {
			d.Names[m.ID] = m.Name
		}
	}
	s.render(w, r, http.StatusOK, "room", d)
}

func (s *Server) roomSounds(r *http.Request, d *roomData) error {
	ctx := r.Context()
	user := sessionFrom(r).Username
	favs, err := s.store.Favorites(ctx, user)
	if err != nil {
		return err
	}
	d.Paging.Keep("c", d.Channel.ID)
	for _, v := range r.URL.Query()["tag"] {
		if t := ParseTags(v); len(t) == 1 && !slices.Contains(d.Tags, t[0]) && len(d.Tags) < maxTags {
			d.Tags = append(d.Tags, t[0])
			d.Paging.Add("tag", t[0])
		}
	}
	if d.SoundTags, err = s.store.SoundTags(ctx, d.Guild.ID, d.Tags); err != nil {
		return err
	}
	// the picked tags stay in the list even when no sound is left
	for _, t := range d.Tags {
		if !slices.ContainsFunc(d.SoundTags, func(x store.SoundTag) bool { return x.Name == t }) {
			d.SoundTags = append(d.SoundTags, store.SoundTag{Name: t})
		}
	}
	if d.Paging.Page == 1 && d.Paging.Query == "" && len(d.Tags) == 0 {
		list, err := s.store.FavoriteSounds(ctx, user)
		if err != nil {
			return err
		}
		for _, m := range list {
			if m.Kind == store.KindSound {
				d.Favorites = append(d.Favorites, soundTile{Media: m, Fav: true})
			}
		}
	}
	list, err := s.store.FindMedia(ctx, store.MediaQuery{Scope: store.All, Offer: d.Guild.ID, Kind: store.KindSound, AllTags: d.Tags, Search: d.Paging.Query,
		Limit: d.Paging.Limit(), Offset: d.Paging.Offset()})
	if err != nil {
		return err
	}
	for _, m := range cut(&d.Paging, list) {
		t := soundTile{Media: m, Fav: favs[m.ID]}
		if m.GuildID == d.Guild.ID {
			d.Own = append(d.Own, t)
		} else {
			d.Rest = append(d.Rest, t)
		}
	}
	if t := ParseTags(r.URL.Query().Get("tagedit")); len(t) == 1 {
		d.TagEdit = &store.SoundTag{Name: t[0]}
		if i := slices.IndexFunc(d.SoundTags, func(x store.SoundTag) bool { return x.Name == t[0] }); i >= 0 {
			d.TagEdit = &d.SoundTags[i]
		}
		d.Emojis = s.bot.Emojis(d.Guild.ID)
		if d.Images, err = s.store.ListMedia(ctx, d.Guild.ID, store.KindImage); err != nil {
			return err
		}
	}
	if id, err := strconv.ParseInt(r.URL.Query().Get("edit"), 10, 64); err == nil {
		if m, err := s.store.Media(ctx, id); err == nil && m.Kind == store.KindSound {
			d.Edit, d.Emojis = &m, s.bot.Emojis(d.Guild.ID)
			if d.Groups, err = s.groupChoices(ctx, r, m.GuildID); err != nil {
				return err
			}
			if d.Images, err = s.store.ListMedia(ctx, d.Guild.ID, store.KindImage); err != nil {
				return err
			}
		}
	}
	return nil
}

// usesChannel tells whether a task joins, posts in, waits for or listens to a channel.
func usesChannel(t tasks.Task, id string) bool {
	if t.Trigger.Kind == tasks.Event && t.Trigger.ChannelID == id {
		return true
	}
	return slices.ContainsFunc(t.Steps, func(st tasks.Step) bool { return st.ChannelID == id })
}

// memberTasks tells whether a task names a member in its member filter or
// steps, and whether its filter leaves them out.
func memberTasks(t tasks.Task, id string) (only, except bool) {
	if t.Trigger.Kind == tasks.Event && slices.Contains(t.Trigger.UserIDs, id) {
		if t.Trigger.Except {
			return false, true
		}
		return true, false
	}
	return slices.ContainsFunc(t.Steps, func(st tasks.Step) bool { return st.UserID == id || st.FollowUserID == id }), false
}

func startsFor(tr tasks.Trigger, id string) bool {
	return len(tr.UserIDs) == 0 || slices.Contains(tr.UserIDs, id) != tr.Except
}

// roomSend sends a text to a text channel or a member from the Control Room.
func (s *Server) roomSend(w http.ResponseWriter, r *http.Request) {
	id, ok := s.guildID(w, r)
	if !ok {
		return
	}
	text := strings.TrimSpace(r.PostForm.Get("text"))
	msgID, _ := strconv.ParseInt(r.PostForm.Get("message"), 10, 64)
	if msgID == 0 && (text == "" || utf8.RuneCountInString(text) > 2000) {
		s.fail(w, r, http.StatusBadRequest, "Write a text of up to 2000 characters.")
		return
	}
	if msgID != 0 {
		if _, err := s.store.Message(r.Context(), msgID); err != nil {
			s.storeErr(w, r, err)
			return
		}
		text = ""
	}
	var step tasks.Step
	var target string
	if c := r.PostForm.Get("channel"); c != "" {
		ch, chGuild, ok := s.bot.Channel(c)
		if !ok || ch.Voice || chGuild != id {
			s.fail(w, r, http.StatusBadRequest, "Pick a text channel.")
			return
		}
		if !ch.Usable {
			s.fail(w, r, http.StatusBadRequest, ch.Problem)
			return
		}
		step, target = tasks.Step{Type: tasks.Message, ChannelID: c, Text: text, MessageID: msgID}, "#"+ch.Name
	} else {
		m, ok := s.bot.ResolveMember(id, r.PostForm.Get("member"))
		if !ok || m.Username == "" {
			s.fail(w, r, http.StatusBadRequest, "Pick a member of the server.")
			return
		}
		step, target = tasks.Step{Type: tasks.DM, UserID: m.ID, Text: text, MessageID: msgID}, m.Name
	}
	if err := s.run.SendStepNow(id, step, sessionFrom(r).Username, "Quick message to "+target); err != nil {
		s.fail(w, r, http.StatusBadGateway, "Not sent: "+err.Error())
		return
	}
	s.audit(r, "message.send", "quick message", s.bot.GuildName(id)+" · "+target)
	redirect(w, r, safeNext(r.PostForm.Get("next"), "/room/"+id), "sent")
}

var customEmojiRe = regexp.MustCompile(`^<a?:[A-Za-z0-9_~]{2,32}:[0-9]{15,21}>$`)

// validEmoji accepts one emoji or a custom emoji of a server like <:name:id>.
func validEmoji(s string) bool {
	if s == "" || customEmojiRe.MatchString(s) {
		return true
	}
	if utf8.RuneCountInString(s) > maxEmojiRunes {
		return false
	}
	for _, r := range s {
		if r < 0x2000 && r != 0x00a9 && r != 0x00ae && r != '#' && r != '*' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func (s *Server) setFavorite(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	if err := s.store.SetFavorite(r.Context(), sessionFrom(r).Username, id, r.PostForm.Get("on") == "1"); err != nil {
		s.storeErr(w, r, err)
		return
	}
	redirect(w, r, safeNext(r.PostForm.Get("next"), "/media"), "")
}

// memberGroup is a heading of the member list with its members, as Discord groups them.
type memberGroup struct {
	Name    string
	Members []bot.Member
}

// memberList is the member column: with a search a page of the matches,
// else the members in voice and then those online, membersPerPage at most,
// grouped like Discord does: by the highest role shown separately, then
// Online and Offline. Without the presence intent the online status is
// unknown and everyone fills up.
func memberList(members []bot.Member, query string, page int, presence bool) (groups []memberGroup, hidden int, more bool) {
	humans := slices.DeleteFunc(slices.Clone(members), func(m bot.Member) bool { return m.Bot })
	if query != "" {
		hits := bot.SearchMembers(humans, query, len(humans))
		from := min((page-1)*membersPerPage, len(hits))
		to := min(from+membersPerPage, len(hits))
		if from == to {
			return nil, 0, false
		}
		return []memberGroup{{Name: "Matches", Members: hits[from:to]}}, 0, to < len(hits)
	}
	// higher roles first when not everyone fits
	slices.SortStableFunc(humans, func(a, b bot.Member) int { return cmp.Compare(groupRank(b), groupRank(a)) })
	var picked []bot.Member
	for _, m := range humans {
		if m.VoiceID != "" && len(picked) < membersPerPage {
			picked = append(picked, m)
		}
	}
	for _, m := range humans {
		if m.VoiceID == "" && (m.Online || !presence) && len(picked) < membersPerPage {
			picked = append(picked, m)
		}
	}
	rest, offline := "Online", "Offline"
	if !presence {
		rest = "Members"
	}
	pos := map[string]int{}
	byName := map[string]*memberGroup{}
	var order []string
	slices.SortStableFunc(picked, func(a, b bot.Member) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	for _, m := range picked {
		name := rest
		switch {
		case presence && !m.Online:
			name = offline
		case m.Group != "":
			name, pos[m.Group] = m.Group, m.GroupPos
		}
		g, ok := byName[name]
		if !ok {
			g = &memberGroup{Name: name}
			byName[name] = g
			order = append(order, name)
		}
		g.Members = append(g.Members, m)
	}
	rank := func(name string) int {
		switch name {
		case rest:
			return -1
		case offline:
			return -2
		}
		return pos[name]
	}
	slices.SortStableFunc(order, func(a, b string) int { return cmp.Compare(rank(b), rank(a)) })
	for _, name := range order {
		groups = append(groups, *byName[name])
	}
	return groups, len(humans) - len(picked), false
}

// millis reads seconds like 1.25 as milliseconds, 0 for anything else.
func millis(v string) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f < 0 || f > 24*3600 {
		return 0
	}
	return int(f*1000 + 0.5)
}

func groupRank(m bot.Member) int {
	if m.Group == "" {
		return -1
	}
	return m.GroupPos
}

// setTagLook sets the emoji or image shown for a tag on the soundboard.
func (s *Server) setTagLook(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	tags := ParseTags(f.Get("tag"))
	if len(tags) != 1 {
		s.fail(w, r, http.StatusBadRequest, "Pick a tag.")
		return
	}
	emoji := strings.TrimSpace(f.Get("emoji"))
	if !validEmoji(emoji) {
		s.fail(w, r, http.StatusBadRequest, "Use one emoji.")
		return
	}
	var icon int64
	if v := f.Get("icon"); v != "" && v != "0" {
		icon, _ = strconv.ParseInt(v, 10, 64)
		img, err := s.store.Media(r.Context(), icon)
		if err != nil || img.Kind != store.KindImage {
			s.fail(w, r, http.StatusBadRequest, "Pick an image from the media library.")
			return
		}
		emoji = ""
	}
	if err := s.store.SetTagLook(r.Context(), tags[0], emoji, icon); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r, "tag.update", tags[0], "icon")
	redirect(w, r, safeNext(f.Get("next"), "/media"), "saved")
}
