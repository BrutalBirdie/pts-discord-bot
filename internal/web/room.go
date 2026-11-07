package web

import (
	"net/http"
	"net/url"
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
	membersPerPage = 50
	maxEmojiRunes  = 10
	// mentions of other members show their ID in the preview
	maxPreviewNames = 2000
)

// soundEmojis are offered in the emoji picker of a sound, any other emoji can be typed.
var soundEmojis = []string{"📯", "👏", "🥁", "👋", "☀️", "🎺", "🔔", "🐟", "🧨", "😠", "🤔", "🎉", "💀", "👑", "🔥", "😂", "🍻", "🚀", "💥", "🎵", "😱", "🙏", "👀", "💤"}

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
	Guilds  []bot.Guild
	Guild   bot.Guild
	Text    []roomChannel
	Voice   []roomChannel
	View    string // voice, text, member or "" for the overview
	Channel bot.Channel
	Member  bot.Member
	Self    string // this page, for the next field of forms
	query   url.Values

	InVoice  []bot.Member
	Others   []bot.Member // members not in voice, one page
	MQuery   string
	MPage    int
	MMore    bool
	Playing  []playingView
	Settings store.Settings

	Favorites []soundTile
	Own       []soundTile // sounds of this server's group
	Rest      []soundTile
	Paging    paging
	Look      *store.Media // the sound whose emoji is being picked
	Emojis    []string
	Images    []store.Media

	Messages []messages.Message
	Author   bot.Member        // the bot in this server, for the message preview
	Names    map[string]string // member names for mentions in the preview
	Tasks    []tasks.Task      // of the channel, or for the member
	Except   []tasks.Task      // that leave the member out
	Greets   []tasks.Task      // event tasks that start when the member joins voice
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

// MPrev and MNext page through the members that are not in voice.
func (d roomData) MPrev() string { return d.mlink(d.MPage - 1) }
func (d roomData) MNext() string { return d.mlink(d.MPage + 1) }

func (d roomData) mlink(page int) string {
	v := url.Values{}
	for k, vs := range d.query {
		v[k] = vs
	}
	v.Set("mp", strconv.Itoa(page))
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
	q := r.URL.Query()
	q.Del("done")
	d := roomData{Guilds: s.guilds(r), Guild: g, Self: r.URL.Path, query: q, Paging: newPaging(r), Emojis: soundEmojis}
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
	for _, c := range s.bot.Channels(g.ID) {
		rc := roomChannel{Channel: c, Current: d.View != "member" && c.ID == d.Channel.ID}
		for _, t := range all {
			if usesChannel(t, c.ID) {
				rc.Tasks++
			}
		}
		if c.Voice {
			d.Voice = append(d.Voice, rc)
		} else {
			d.Text = append(d.Text, rc)
		}
	}

	d.MQuery = strings.TrimSpace(q.Get("mq"))
	d.MPage, _ = strconv.Atoi(q.Get("mp"))
	d.MPage = max(d.MPage, 1)
	var others []bot.Member
	for _, m := range members {
		switch {
		case m.Bot:
		case m.VoiceID != "":
			d.InVoice = append(d.InVoice, m)
		case d.MQuery == "" || len(bot.SearchMembers([]bot.Member{m}, d.MQuery, 1)) == 1:
			others = append(others, m)
		}
	}
	from := min((d.MPage-1)*membersPerPage, len(others))
	to := min(from+membersPerPage, len(others))
	d.Others, d.MMore = others[from:to], to < len(others)

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
				d.Tasks = append(d.Tasks, t)
			}
		}
		if d.View == "text" {
			if d.Messages, err = s.store.ListMessages(ctx, g.ID); err != nil {
				s.internalError(w, r, err)
				return
			}
			d.Author, d.Names = s.author(g.ID), map[string]string{}
			for _, m := range members[:min(len(members), maxPreviewNames)] {
				d.Names[m.ID] = m.Name
			}
		}
	case "member":
		for _, t := range all {
			only, except := memberTasks(t, d.Member.ID)
			if only {
				d.Tasks = append(d.Tasks, t)
			}
			if except {
				d.Except = append(d.Except, t)
			}
			if t.Enabled && t.Trigger.Kind == tasks.Event && t.Trigger.Event == tasks.VoiceJoin && startsFor(t.Trigger, d.Member.ID) {
				d.Greets = append(d.Greets, t)
			}
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
	if d.Paging.Page == 1 && d.Paging.Query == "" {
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
	list, err := s.store.FindMedia(ctx, store.MediaQuery{Scope: store.All, Offer: d.Guild.ID, Kind: store.KindSound, Search: d.Paging.Query,
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
	if id, err := strconv.ParseInt(r.URL.Query().Get("look"), 10, 64); err == nil {
		if m, err := s.store.Media(ctx, id); err == nil {
			d.Look = &m
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

// setMediaLook sets the emoji or image of a sound.
func (s *Server) setMediaLook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	emoji := strings.TrimSpace(r.PostForm.Get("emoji"))
	if typed := strings.TrimSpace(r.PostForm.Get("emoji_text")); typed != "" {
		emoji = typed
	}
	if utf8.RuneCountInString(emoji) > maxEmojiRunes || strings.ContainsAny(emoji, " \t\r\n<>") {
		s.fail(w, r, http.StatusBadRequest, "Use one emoji.")
		return
	}
	var icon int64
	if r.PostForm.Get("clear") == "1" {
		emoji = ""
	} else if v := r.PostForm.Get("icon"); v != "" && v != "0" {
		icon, _ = strconv.ParseInt(v, 10, 64)
		m, err := s.store.Media(r.Context(), icon)
		if err != nil || m.Kind != store.KindImage {
			s.fail(w, r, http.StatusBadRequest, "Pick an image from the media library.")
			return
		}
		emoji = ""
	}
	if err := s.store.SetMediaLook(r.Context(), id, emoji, icon); err != nil {
		s.storeErr(w, r, err)
		return
	}
	redirect(w, r, safeNext(r.PostForm.Get("next"), "/media"), "saved")
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
