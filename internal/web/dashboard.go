package web

import (
	"cmp"
	"net/http"
	"slices"
	"time"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

type upcoming struct {
	At    time.Time
	Task  tasks.Task
	Guild string
	What  string
}

type playingView struct {
	P     bot.Playing
	Guild string
	CSRF  string
	Next  string
}

type dashboardData struct {
	Servers   int
	Members   int
	Active    int
	Paused    int
	Next      *upcoming
	Upcoming  []upcoming
	Runs      []store.Run
	Failed24h int
	Runs24h   int
	Playing   []playingView
}

func (s *Server) playingViews(r *http.Request) []playingView {
	var out []playingView
	for _, p := range s.bot.NowPlaying() {
		out = append(out, playingView{P: p, Guild: s.bot.GuildName(p.GuildID), CSRF: sessionFrom(r).CSRFToken, Next: r.URL.Path})
	}
	slices.SortFunc(out, func(a, b playingView) int { return a.P.Started.Compare(b.P.Started) })
	return out
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.store.ListTasks(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	names, err := s.mediaNames(r)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	d := dashboardData{Playing: s.playingViews(r)}
	for _, g := range s.bot.Guilds() {
		d.Servers++
		d.Members += g.Cached
	}
	now := time.Now()
	for _, t := range list {
		if !t.Enabled {
			d.Paused++
			continue
		}
		d.Active++
		for _, at := range t.Trigger.Next(now, 12) {
			if at.Sub(now) > 24*time.Hour {
				break
			}
			d.Upcoming = append(d.Upcoming, upcoming{At: at, Task: t, Guild: s.bot.GuildName(t.GuildID), What: s.describeSteps(t, names)})
		}
	}
	slices.SortFunc(d.Upcoming, func(a, b upcoming) int { return cmp.Compare(a.At.UnixNano(), b.At.UnixNano()) })
	if len(d.Upcoming) > 0 {
		d.Next = &d.Upcoming[0]
	}
	if len(d.Upcoming) > 8 {
		d.Upcoming = d.Upcoming[:8]
	}
	if d.Runs, err = s.store.ListRuns(ctx, store.RunQuery{Limit: 8}); err != nil {
		s.internalError(w, r, err)
		return
	}
	day := store.RunQuery{Since: now.Add(-24 * time.Hour)}
	if d.Runs24h, err = s.store.CountRuns(ctx, day); err != nil {
		s.internalError(w, r, err)
		return
	}
	day.Status = store.RunFailed
	if d.Failed24h, err = s.store.CountRuns(ctx, day); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "dashboard", d)
}
