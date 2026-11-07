package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/media"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

type serversData struct {
	Guilds    []bot.Guild
	InviteURL string
	Paging    paging
}

func (s *Server) servers(w http.ResponseWriter, r *http.Request) {
	d := serversData{Paging: newPaging(r)}
	if !sessionFrom(r).Discord() {
		d.InviteURL = s.bot.InviteURL()
	}
	var shown []bot.Guild
	for _, g := range s.guilds(r) {
		if d.Paging.Matches(g.Name, g.ID) {
			shown = append(shown, g)
		}
	}
	d.Guilds = window(&d.Paging, shown)
	s.render(w, r, http.StatusOK, "servers", d)
}

func (s *Server) refreshServers(w http.ResponseWriter, r *http.Request) {
	if !s.bot.Status().Connected {
		s.fail(w, r, http.StatusServiceUnavailable, bot.ErrNotConnected.Error()+".")
		return
	}
	// chunking large servers takes a while, the page does not wait for it
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.bot.RefreshMembers(ctx); err != nil {
			s.log.Error("refreshing members", "err", err)
		}
	}()
	redirect(w, r, safeNext(r.PostForm.Get("next"), "/servers"), "refreshed")
}

type serverData struct {
	Guild    bot.Guild
	Tab      string
	Channels []bot.Channel
	Text     []bot.Channel
	Voice    []bot.Channel
	Tasks    []tasks.Task
	Sounds   []store.Media
	Playing  []playingView
	Paging   paging // of the tasks tab
}

func (s *Server) server(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	g, ok := s.bot.Guild(r.PathValue("id"))
	if !ok || !sess.CanGuild(g.ID) {
		s.notFound(w, r)
		return
	}
	d := serverData{Guild: g, Tab: r.URL.Query().Get("tab"), Paging: newPaging(r)}
	for _, p := range s.playingViews(r) {
		if p.P.GuildID == g.ID {
			d.Playing = append(d.Playing, p)
		}
	}
	switch d.Tab {
	case "tasks":
		list, err := s.store.ListTasks(r.Context(), store.GuildScope(g.ID))
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		d.Paging.Keep("tab", d.Tab)
		for _, t := range list {
			if t.GuildID == g.ID && d.Paging.Matches(t.Name) {
				d.Tasks = append(d.Tasks, t)
			}
		}
		d.Tasks = window(&d.Paging, d.Tasks)
	default:
		d.Tab = "channels"
		for _, c := range s.bot.Channels(g.ID) {
			if c.Voice {
				d.Voice = append(d.Voice, c)
			} else {
				d.Text = append(d.Text, c)
			}
		}
		sounds, err := s.store.ListMedia(r.Context(), g.ID, "")
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		for _, m := range sounds {
			if media.HasAudio(m) {
				d.Sounds = append(d.Sounds, m)
			}
		}
	}
	s.render(w, r, http.StatusOK, "server", d)
}

type memberHit struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

// searchMembers feeds the member pickers. Without a guild it searches all of them.
func (s *Server) searchMembers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var all []bot.Member
	if g := q.Get("guild"); g != "" {
		if sessionFrom(r).CanGuild(g) {
			all = s.bot.Members(g)
		}
	} else {
		seen := map[string]bool{}
		for _, g := range s.guilds(r) {
			for _, m := range s.bot.Members(g.ID) {
				if !seen[m.ID] {
					seen[m.ID] = true
					all = append(all, m)
				}
			}
		}
	}
	hits := []memberHit{}
	for _, m := range bot.SearchMembers(all, q.Get("q"), 15) {
		hits = append(hits, memberHit{ID: m.ID, Label: m.Label(), Name: m.Name, Username: m.Username})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(hits)
}

// guildID reads the server of a voice control, not found for servers the user may not manage.
func (s *Server) guildID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	gid, ok := bot.ParseID(id)
	if !ok || !sessionFrom(r).CanGuild(gid.String()) {
		s.notFound(w, r)
		return "", false
	}
	return gid.String(), true
}

func (s *Server) stopVoice(w http.ResponseWriter, r *http.Request) {
	id, ok := s.guildID(w, r)
	if !ok {
		return
	}
	gid, _ := bot.ParseID(id)
	if s.bot.Stop(gid) {
		s.audit(r, "voice.stop", s.bot.GuildName(id), "")
	}
	redirect(w, r, safeNext(r.PostForm.Get("next"), "/servers/"+id), "stopped")
}

func (s *Server) leaveVoice(w http.ResponseWriter, r *http.Request) {
	id, ok := s.guildID(w, r)
	if !ok {
		return
	}
	gid, _ := bot.ParseID(id)
	if err := s.bot.Leave(r.Context(), gid); err != nil {
		s.fail(w, r, http.StatusBadGateway, bot.Describe(err))
		return
	}
	s.audit(r, "voice.leave", s.bot.GuildName(id), "")
	redirect(w, r, safeNext(r.PostForm.Get("next"), "/servers/"+id), "left")
}

func (s *Server) joinVoice(w http.ResponseWriter, r *http.Request) {
	id, ok := s.guildID(w, r)
	if !ok {
		return
	}
	channelID := r.PostForm.Get("channel")
	ch, chGuild, ok := s.bot.Channel(channelID)
	if !ok || !ch.Voice || chGuild != id {
		s.fail(w, r, http.StatusBadRequest, "Pick a voice channel.")
		return
	}
	if !ch.Usable {
		s.fail(w, r, http.StatusBadRequest, ch.Problem)
		return
	}
	s.run.JoinNow(id, channelID, sessionFrom(r).Username)
	s.audit(r, "voice.join", s.bot.GuildName(id), ch.Name)
	redirect(w, r, "/servers/"+id, "joining")
}

// playInChannel plays a file from the media library in a voice channel.
func (s *Server) playInChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := s.guildID(w, r)
	if !ok {
		return
	}
	s.startPlay(w, r, id, r.PostForm.Get("channel"), r.PostForm.Get("media"))
}

func (s *Server) startPlay(w http.ResponseWriter, r *http.Request, guildID, channelID, mediaID string) {
	sess := sessionFrom(r)
	mid, _ := strconv.ParseInt(mediaID, 10, 64)
	m, err := s.store.Media(r.Context(), mid)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusBadRequest, "Pick a sound.")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ch, chGuild, ok := s.bot.Channel(channelID)
	if !ok || !ch.Voice || (guildID != "" && chGuild != guildID) || !sess.CanGuild(chGuild) {
		s.fail(w, r, http.StatusBadRequest, "Pick a voice channel.")
		return
	}
	if !ch.Usable {
		s.fail(w, r, http.StatusBadRequest, ch.Problem)
		return
	}
	s.run.PlayNow(chGuild, channelID, m, r.PostForm.Get("leave") == "on", sessionFrom(r).Username)
	s.audit(r, "voice.play", m.Name, s.bot.GuildName(chGuild)+" · "+ch.Name)
	redirect(w, r, safeNext(r.PostForm.Get("next"), "/"), "playing")
}
