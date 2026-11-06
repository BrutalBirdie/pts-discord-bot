package web

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// outside are the items the steps of a task reference but may not use:
// shared ones for Discord users, those of other servers for everyone.
type outside struct {
	discord bool
	media   map[int64]store.Media
	msgs    map[int64]messages.Message
	tags    []string // tags of play steps only shared sounds have
}

func (s *Server) findOutside(ctx context.Context, sess *store.Session, t tasks.Task) (outside, error) {
	o := outside{discord: sess.Discord(), media: map[int64]store.Media{}, msgs: map[int64]messages.Message{}}
	sc := sess.ItemScope(t.GuildID)
	var ids []int64
	var tags []string
	for _, st := range t.Steps {
		ids = append(ids, st.MediaIDs...)
		if st.AvatarMediaID != 0 {
			ids = append(ids, st.AvatarMediaID)
		}
		if st.Tag != "" {
			tags = append(tags, st.Tag)
		}
		if st.MessageID == 0 || st.Type != tasks.Message && st.Type != tasks.DM {
			continue
		}
		m, err := s.store.Message(ctx, st.MessageID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return o, err
		}
		if !sc.Has(m.GuildID) {
			o.msgs[m.ID] = m
		}
	}
	list, err := s.store.MediaByIDs(ctx, ids)
	if err != nil {
		return o, err
	}
	for _, m := range list {
		if !sc.Has(m.GuildID) {
			o.media[m.ID] = m
		}
	}
	if len(tags) == 0 || !sess.Discord() {
		return o, nil
	}
	own, err := s.store.Tags(ctx, sc)
	if err != nil {
		return o, err
	}
	shared, err := s.store.Tags(ctx, store.GuildScope(true))
	if err != nil {
		return o, err
	}
	for _, tag := range tags {
		if !slices.Contains(own, tag) && slices.Contains(shared, tag) {
			o.tags = append(o.tags, tag)
		}
	}
	return o, nil
}

// label stands in for the name of an item outside, which Discord users may not see.
func (o outside) label(noun, guildID, name string) string {
	switch {
	case !o.discord:
		return name
	case guildID == "":
		return "Shared " + noun
	default:
		return capitalize(noun) + " of another server"
	}
}

func (o outside) problem(noun, guildID, name string) string {
	switch {
	case guildID == "":
		return "Shared " + noun + ", only an admin can use it. Ask an admin to move it to this server."
	case o.discord:
		return capitalize(noun) + " of another server, remove it."
	default:
		return capitalize(noun) + " " + name + " belongs to another server, remove it or move it to this server."
	}
}

// issues explains why a step cannot use what it references.
func (o outside) issues(st tasks.Step) []string {
	var out []string
	add := func(msg string) {
		if !slices.Contains(out, msg) {
			out = append(out, msg)
		}
	}
	switch st.Type {
	case tasks.Play:
		if slices.Contains(o.tags, st.Tag) {
			add("Shared sounds tagged " + st.Tag + ", only an admin can use them. Ask an admin to move them to this server.")
		}
		for _, id := range st.MediaIDs {
			if m, ok := o.media[id]; ok {
				add(o.problem("sound", m.GuildID, m.Name))
			}
		}
	case tasks.Profile:
		if m, ok := o.media[st.AvatarMediaID]; ok {
			add(o.problem("image", m.GuildID, m.Name))
		}
	case tasks.Message, tasks.DM:
		if m, ok := o.msgs[st.MessageID]; ok {
			add(o.problem("message", m.GuildID, m.Name))
		}
	}
	return out
}

// problems are the issues of all steps, for the Not saved list.
func (o outside) problems(t tasks.Task) []string {
	var out []string
	for i, st := range t.Steps {
		for _, msg := range o.issues(st) {
			out = append(out, fmt.Sprintf("Step %d: %s", i+1, msg))
		}
	}
	return out
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
