package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// moveTargets are the servers a Cloudron user can move files and messages
// to, also those the bot is not connected to right now.
func (s *Server) moveTargets(ctx context.Context) ([]bot.Guild, error) {
	gs := s.bot.Guilds()
	known, err := s.store.KnownGuilds(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range known {
		if !slices.ContainsFunc(gs, func(g bot.Guild) bool { return g.ID == id }) {
			gs = append(gs, bot.Guild{ID: id, Name: s.bot.GuildName(id)})
		}
	}
	return gs, nil
}

// moveTarget reads the server to move to, "" for shared. Moving is for
// Cloudron users only, Discord users get not found.
func (s *Server) moveTarget(w http.ResponseWriter, r *http.Request) (string, bool) {
	target := r.PostForm.Get("guild")
	if sessionFrom(r).Discord() {
		s.notFound(w, r)
		return "", false
	}
	if _, ok := bot.ParseID(target); target != "" && !ok {
		s.fail(w, r, http.StatusBadRequest, "Pick a server.")
		return "", false
	}
	return target, true
}

// ownerLabel names an owner for texts, "shared" for shared items.
func (s *Server) ownerLabel(guildID string) string {
	if guildID == "" {
		return "shared"
	}
	return s.bot.GuildName(guildID)
}

func (s *Server) useText(u store.Use) string {
	return u.Kind + " " + u.Name + " (" + s.ownerLabel(u.GuildID) + ")"
}

// movingWarning is the confirmation text of a move: the users of the items,
// those the chosen server cannot serve fail afterwards.
func (s *Server) movingWarning(uses []store.Use) string {
	if len(uses) == 0 {
		return ""
	}
	names := make([]string, len(uses))
	for i, u := range uses {
		names[i] = s.useText(u)
	}
	return "Used by " + strings.Join(names, ", ") + ". Those that cannot use the chosen server's items fail to run after the move."
}

// broken lists the users that fail once the items belong to target.
func (s *Server) broken(uses []store.Use, target string) []string {
	var out []string
	for _, u := range uses {
		if u.Breaks(target) {
			out = append(out, s.useText(u))
		}
	}
	return out
}

func (s *Server) moveErr(w http.ResponseWriter, r *http.Request, target string, err error) {
	var taken *store.NameTakenError
	if errors.As(err, &taken) {
		owner := s.ownerLabel(target)
		if target == "" {
			owner = "the shared library"
		}
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("Not moved: %s has a file named %s already. Rename one of them first.", owner, taken.Name))
		return
	}
	s.storeErr(w, r, err)
}

func (s *Server) moveDetail(target string, broken []string) string {
	d := "to " + s.ownerLabel(target)
	if len(broken) > 0 {
		d += ", fails now: " + strings.Join(broken, ", ")
	}
	return d
}

// moveMedia moves one file, or with the id fields the files selected on the media page.
func (s *Server) moveMedia(w http.ResponseWriter, r *http.Request) {
	target, ok := s.moveTarget(w, r)
	if !ok {
		return
	}
	var ids []int64
	if r.PathValue("id") != "" {
		m, ok := s.loadMedia(w, r)
		if !ok {
			return
		}
		ids = []int64{m.ID}
	} else {
		for _, v := range r.PostForm["id"] {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		s.fail(w, r, http.StatusBadRequest, "Select the files to move.")
		return
	}
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
	uses, err := s.store.MediaUses(ctx, ids)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.store.MoveItems(ctx, target, ids, nil); err != nil {
		s.moveErr(w, r, target, err)
		return
	}
	for _, m := range list {
		if m.GuildID != target {
			s.audit(r, "media.move", m.Name, s.moveDetail(target, s.broken(uses[m.ID], target)))
		}
	}
	redirect(w, r, safeNext(r.PostForm.Get("next"), "/media"), "moved")
}

func (s *Server) moveMessage(w http.ResponseWriter, r *http.Request) {
	target, ok := s.moveTarget(w, r)
	if !ok {
		return
	}
	m, ok := s.loadMessage(w, r)
	if !ok {
		return
	}
	uses, err := s.store.MessageUses(r.Context(), m.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.store.MoveItems(r.Context(), target, nil, []int64{m.ID}); err != nil {
		s.moveErr(w, r, target, err)
		return
	}
	if m.GuildID != target {
		s.audit(r, "message.move", m.Name, s.moveDetail(target, s.broken(uses, target)))
	}
	redirect(w, r, fmt.Sprintf("/messages/%d", m.ID), "moved")
}

// sharedOfTask are the shared files and messages a task uses: picked
// sounds and avatars, sounds of its tags and saved messages.
func (s *Server) sharedOfTask(ctx context.Context, t tasks.Task) ([]store.Media, []messages.Message, error) {
	var ids []int64
	var tags []string
	var msgs []messages.Message
	for _, st := range t.Steps {
		ids = append(ids, st.MediaIDs...)
		if st.AvatarMediaID != 0 {
			ids = append(ids, st.AvatarMediaID)
		}
		if st.Tag != "" {
			tags = append(tags, st.Tag)
		}
		if st.MessageID != 0 && (st.Type == tasks.Message || st.Type == tasks.DM) && !slices.ContainsFunc(msgs, func(m messages.Message) bool { return m.ID == st.MessageID }) {
			m, err := s.store.Message(ctx, st.MessageID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			if m.GuildID == "" {
				msgs = append(msgs, m)
			}
		}
	}
	if len(tags) > 0 {
		shared, err := s.store.ListMedia(ctx, store.GuildScope(true), "")
		if err != nil {
			return nil, nil, err
		}
		for _, m := range shared {
			if slices.ContainsFunc(m.Tags, func(tag string) bool { return slices.Contains(tags, tag) }) {
				ids = append(ids, m.ID)
			}
		}
	}
	list, err := s.store.MediaByIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	list = slices.DeleteFunc(list, func(m store.Media) bool { return m.GuildID != "" })
	return list, msgs, nil
}

// claim is the quick fix on the task editor: move the shared items of a task to its server.
type claim struct {
	Names   []string
	Server  string
	Warning string
}

func (s *Server) taskClaim(ctx context.Context, t tasks.Task) (claim, error) {
	c := claim{Server: s.ownerLabel(t.GuildID)}
	med, msgs, err := s.sharedOfTask(ctx, t)
	if err != nil || len(med)+len(msgs) == 0 {
		return c, err
	}
	ids := make([]int64, len(med))
	for i, m := range med {
		ids[i] = m.ID
		c.Names = append(c.Names, m.Kind+" "+m.Name)
	}
	uses, err := s.store.MediaUses(ctx, ids)
	if err != nil {
		return c, err
	}
	var broken []string
	for _, id := range ids {
		broken = append(broken, s.broken(uses[id], t.GuildID)...)
	}
	for _, m := range msgs {
		c.Names = append(c.Names, "message "+m.Name)
		u, err := s.store.MessageUses(ctx, m.ID)
		if err != nil {
			return c, err
		}
		broken = append(broken, s.broken(u, t.GuildID)...)
	}
	slices.Sort(broken)
	if broken = slices.Compact(broken); len(broken) > 0 {
		c.Warning = " These then fail to run: " + strings.Join(broken, ", ") + "."
	}
	return c, nil
}

// claimTask moves the shared items of a task to its server, for Cloudron users.
func (s *Server) claimTask(w http.ResponseWriter, r *http.Request) {
	if sessionFrom(r).Discord() {
		s.notFound(w, r)
		return
	}
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	med, msgs, err := s.sharedOfTask(ctx, t)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var mediaIDs, msgIDs []int64
	for _, m := range med {
		mediaIDs = append(mediaIDs, m.ID)
	}
	for _, m := range msgs {
		msgIDs = append(msgIDs, m.ID)
	}
	uses, err := s.store.MediaUses(ctx, mediaIDs)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.store.MoveItems(ctx, t.GuildID, mediaIDs, msgIDs); err != nil {
		s.moveErr(w, r, t.GuildID, err)
		return
	}
	for _, m := range med {
		s.audit(r, "media.move", m.Name, s.moveDetail(t.GuildID, s.broken(uses[m.ID], t.GuildID)))
	}
	for _, m := range msgs {
		u, err := s.store.MessageUses(ctx, m.ID)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		s.audit(r, "message.move", m.Name, s.moveDetail(t.GuildID, s.broken(u, t.GuildID)))
	}
	redirect(w, r, fmt.Sprintf("/tasks/%d", t.ID), "moved")
}
