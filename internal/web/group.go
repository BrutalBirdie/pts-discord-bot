package web

import (
	"context"
	"net/http"
	"slices"
	"strconv"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
)

// groupChoices are the servers a file or message can be grouped with:
// for Cloudron users every server the bot knows, also those it is not
// connected to right now, for Discord users their approved servers. The
// current group stays a choice.
func (s *Server) groupChoices(ctx context.Context, r *http.Request, current string) ([]bot.Guild, error) {
	sess := sessionFrom(r)
	gs := s.guilds(r)
	extra := slices.Clone(sess.Granted)
	if !sess.Discord() {
		known, err := s.store.KnownGuilds(ctx)
		if err != nil {
			return nil, err
		}
		extra = known
	}
	if current != "" {
		extra = append(extra, current)
	}
	for _, id := range extra {
		if !slices.ContainsFunc(gs, func(g bot.Guild) bool { return g.ID == id }) {
			gs = append(gs, bot.Guild{ID: id, Name: s.bot.GuildName(id)})
		}
	}
	return gs, nil
}

// validGroup tells whether the user may group a file or message with
// guildID, "" for none. Discord users pick one of their servers or keep the
// current group.
func validGroup(sess *store.Session, guildID, current string) bool {
	if guildID == "" || guildID == current {
		return true
	}
	if _, ok := bot.ParseID(guildID); !ok {
		return false
	}
	return sess.CanGuild(guildID)
}

// groupName names a server group for texts, "no server" for none.
func (s *Server) groupName(guildID string) string {
	if guildID == "" {
		return "no server"
	}
	return s.bot.GuildName(guildID)
}

func groupDetail(name string, only bool) string {
	if only {
		return name + ", not suggested to other servers"
	}
	return name
}

// groupMedia sets the server group of the files selected on the media page.
func (s *Server) groupMedia(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	for _, v := range r.PostForm["id"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		s.fail(w, r, http.StatusBadRequest, "Select the files first.")
		return
	}
	guildID, only := r.PostForm.Get("guild"), r.PostForm.Get("group_only") == "on"
	ctx := r.Context()
	list, err := s.store.MediaByIDs(ctx, ids)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if len(list) != len(ids) {
		s.notFound(w, r)
		return
	}
	sess := sessionFrom(r)
	for _, m := range list {
		if !validGroup(sess, guildID, m.GuildID) {
			s.fail(w, r, http.StatusBadRequest, "Pick one of your servers.")
			return
		}
	}
	if err := s.store.SetGroup(ctx, guildID, only, ids, nil); err != nil {
		s.storeErr(w, r, err)
		return
	}
	for _, m := range list {
		if m.GuildID != guildID || m.GroupOnly != (only && guildID != "") {
			s.audit(r, "media.group", m.Name, groupDetail(s.groupName(guildID), only && guildID != ""))
		}
	}
	redirect(w, r, safeNext(r.PostForm.Get("next"), "/media"), "grouped")
}
