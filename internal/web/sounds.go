package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/BrutalBirdie/pts-discord-bot/internal/media"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

type soundHit struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	DurationMS int    `json:"duration_ms"`
}

// searchSounds feeds the sound picker of play steps.
func (s *Server) searchSounds(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.store.SearchSounds(ctx, r.URL.Query().Get("guild"), strings.TrimSpace(r.URL.Query().Get("q")), 15)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	hits := []soundHit{}
	for _, m := range list {
		hits = append(hits, soundHit{ID: m.ID, Name: m.Name, DurationMS: m.DurationMS})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(hits)
}

// savePreviewVolume stores the volume of every sound preview of the user.
func (s *Server) savePreviewVolume(w http.ResponseWriter, r *http.Request) {
	vol, err := strconv.Atoi(r.PostForm.Get("volume"))
	if err != nil || vol < 0 || vol > 100 {
		http.Error(w, "a volume from 0 to 100 is needed", http.StatusBadRequest)
		return
	}
	if err := s.store.SetPreviewVolume(r.Context(), sessionFrom(r).Username, vol); err != nil {
		s.log.Error("saving preview volume", "err", err)
		http.Error(w, "not saved", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// token is one chip of a token field.
type token struct {
	Value  string // the hidden input
	Name   string
	Sub    string // muted text after the name
	Title  string
	Avatar string // initial of a member
	Media  int64  // sounds get a preview button
	MS     int    // length, for the live total
}

// tokenField is the data of the "tokens" partial.
type tokenField struct {
	Field       string // name of the hidden inputs
	Find        string // name of the search input
	Value       string // text of the search input
	Label       string
	LabelID     string // shows Label above the field
	Placeholder string
	Empty       string // count text without tokens
	Help        string
	Source      string // suggestions URL, /members when empty
	Guild       string
	Class       string
	Max         int
	Dupes       bool
	Items       []token
}

func (e taskEditor) MemberTokens() tokenField {
	f := tokenField{Field: "ev_member", Find: "ev_member", Label: "Members", LabelID: "ev-members", Placeholder: "Search members",
		Empty: "Any member", Help: "Empty: any member starts the task.", Guild: e.Task.GuildID, Max: tasks.MaxEventMembers}
	for _, m := range e.EvMembers {
		k := token{Value: m.ID, Name: m.Name, Title: m.Name, Avatar: initial(m.Name)}
		if m.Username != "" {
			k.Sub = "@" + m.Username
			k.Title += " (@" + m.Username + ")"
		}
		f.Items = append(f.Items, k)
	}
	return f
}

// playView is a play step in the editor.
type playView struct {
	Tokens  tokenField
	Total   int // ms
	Cap     int // ms
	Unknown bool
	Over    bool
}

func (e taskEditor) Play(i int) playView {
	st := e.Task.Steps[i]
	p := "s" + strconv.Itoa(i) + "."
	v := playView{Tokens: tokenField{Field: p + "media", Find: p + "find", Label: "Sounds", Placeholder: "Search sounds", Empty: "No sound",
		Source: "/sounds", Guild: e.Task.GuildID, Class: "wide", Max: tasks.MaxSounds, Dupes: true, Value: st.Find}}
	byID := soundsByID(e.Sounds)
	for _, ref := range st.Sounds() {
		ms, _ := refLength(ref, byID, e.Sounds)
		k := token{MS: ms, Sub: "length unknown"}
		if ms > 0 {
			k.Sub = clock(ms)
		}
		if ref.Tag != "" {
			k.Value, k.Name = "tag:"+ref.Tag, "random sound tagged "+ref.Tag
		} else if m, ok := byID[ref.MediaID]; ok {
			k.Value, k.Name, k.Media = strconv.FormatInt(m.ID, 10), m.Name, m.ID
		} else {
			k.Value, k.Name, k.Sub = strconv.FormatInt(ref.MediaID, 10), "Deleted sound", ""
		}
		k.Title = k.Name
		v.Tokens.Items = append(v.Tokens.Items, k)
	}
	minutes := e.MaxPlayMinutes
	if minutes <= 0 {
		minutes = store.DefaultSettings.MaxPlayMinutes
	}
	v.Cap = minutes * 60000
	v.Total, v.Unknown = playLength(st, e.Sounds)
	v.Over = v.Total > v.Cap
	return v
}

func soundsByID(sounds []store.Media) map[int64]store.Media {
	out := make(map[int64]store.Media, len(sounds))
	for _, m := range sounds {
		out[m.ID] = m
	}
	return out
}

// refLength is the length of a play step entry in ms, the longest sound of a tag.
func refLength(ref tasks.SoundRef, byID map[int64]store.Media, sounds []store.Media) (ms int, unknown bool) {
	if ref.Tag == "" {
		m := byID[ref.MediaID]
		return m.DurationMS, m.DurationMS == 0
	}
	found := false
	for _, m := range sounds {
		if slices.Contains(m.Tags, ref.Tag) {
			found = true
			ms = max(ms, m.DurationMS)
			unknown = unknown || m.DurationMS == 0
		}
	}
	return ms, unknown || !found
}

// playLength is the longest a play step plays: the sum of a sequence,
// the longest entry of a random pick. unknown is set when a length is missing.
func playLength(st tasks.Step, sounds []store.Media) (ms int, unknown bool) {
	byID := soundsByID(sounds)
	for _, ref := range st.Sounds() {
		n, u := refLength(ref, byID, sounds)
		unknown = unknown || u
		if st.Order == tasks.OrderRandom {
			ms = max(ms, n)
		} else {
			ms += n
		}
	}
	return ms, unknown
}

// playProblems checks the sounds of play steps against the library and the longest playback.
func playProblems(t tasks.Task, sounds []store.Media, maxPlayMinutes int) []string {
	var out []string
	byID := soundsByID(sounds)
	for i, st := range t.Steps {
		if st.Type != tasks.Play {
			continue
		}
		for _, id := range st.MediaIDs {
			if _, ok := byID[id]; !ok {
				out = append(out, fmt.Sprintf("Step %d: a picked sound was deleted from the media library, remove it.", i+1))
				break
			}
		}
		ms, _ := playLength(st, sounds)
		if ms <= maxPlayMinutes*60000 {
			continue
		}
		what := "sounds add up to"
		if st.Order == tasks.OrderRandom {
			what = "the longest sound is"
		}
		out = append(out, fmt.Sprintf("Step %d: %s %s, longest playback is %d min.", i+1, what, clock(ms), maxPlayMinutes))
	}
	return out
}

// resolveSounds adds the sounds typed by name in the editor without JavaScript.
func (s *Server) resolveSounds(ctx context.Context, t *tasks.Task) ([]string, error) {
	var bad []string
	for i := range t.Steps {
		st := &t.Steps[i]
		if st.Find == "" {
			continue
		}
		m, err := s.store.MediaByName(ctx, st.Find)
		if errors.Is(err, store.ErrNotFound) || err == nil && !media.HasAudio(m) {
			bad = append(bad, fmt.Sprintf("Step %d: no sound is named %q. Pick one from the suggestions.", i+1, st.Find))
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(st.Sounds()) < tasks.MaxSounds {
			st.MediaIDs = append(st.MediaIDs, m.ID)
		}
		st.Find = ""
	}
	return bad, nil
}

// removeToken handles the remove buttons of token fields, op tokdel:<field>:<index>.
func removeToken(t *tasks.Task, arg string) {
	cut := strings.LastIndex(arg, ":")
	if cut < 0 {
		return
	}
	field := arg[:cut]
	i, err := strconv.Atoi(arg[cut+1:])
	if err != nil || i < 0 {
		return
	}
	if field == "ev_member" {
		if i < len(t.Trigger.UserIDs) {
			t.Trigger.UserIDs = slices.Delete(t.Trigger.UserIDs, i, i+1)
		}
		return
	}
	rest, ok := strings.CutPrefix(field, "s")
	rest, ok2 := strings.CutSuffix(rest, ".media")
	n, err := strconv.Atoi(rest)
	if !ok || !ok2 || err != nil || n < 0 || n >= len(t.Steps) {
		return
	}
	st := &t.Steps[n]
	if st.Tag != "" {
		if i == 0 {
			st.Tag = ""
			return
		}
		i--
	}
	if i < len(st.MediaIDs) {
		st.MediaIDs = slices.Delete(st.MediaIDs, i, i+1)
	}
}
