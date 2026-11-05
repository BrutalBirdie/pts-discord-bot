package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
)

type adminData struct {
	Tab        string
	Admins     []string
	Groups     []string
	Settings   store.Settings
	Text       []guildChannels
	MediaCount int
	MediaBytes int64
	Audit      []store.AuditEntry
	Search     string
	Category   string
	Page       int
	More       bool
	Timezones  []string
}

const auditPerPage = 50

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := adminData{Tab: r.PathValue("tab"), Admins: s.cfg.Admins.Users, Groups: s.cfg.Admins.Groups, Timezones: commonTimezones}
	var err error
	if d.Settings, err = s.store.Settings(ctx); err != nil {
		s.internalError(w, r, err)
		return
	}
	switch d.Tab {
	case "", "overview":
		d.Tab = "overview"
		if d.MediaCount, d.MediaBytes, err = s.store.MediaBytes(ctx); err != nil {
			s.internalError(w, r, err)
			return
		}
	case "settings":
		for _, g := range s.bot.Guilds() {
			gc := guildChannels{Guild: g}
			for _, c := range s.bot.Channels(g.ID) {
				if !c.Voice && c.Usable {
					gc.Text = append(gc.Text, c)
				}
			}
			d.Text = append(d.Text, gc)
		}
	case "audit":
		q := r.URL.Query()
		d.Search, d.Category = strings.TrimSpace(q.Get("q")), q.Get("category")
		d.Page, _ = strconv.Atoi(q.Get("page"))
		d.Page = max(d.Page, 1)
		list, err := s.store.ListAudit(ctx, store.AuditQuery{Category: d.Category, Search: d.Search, Limit: auditPerPage + 1, Offset: (d.Page - 1) * auditPerPage})
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if d.More = len(list) > auditPerPage; d.More {
			list = list[:auditPerPage]
		}
		d.Audit = list
	default:
		s.notFound(w, r)
		return
	}
	s.render(w, r, http.StatusOK, "admin", d)
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	st := store.Settings{Timezone: strings.TrimSpace(f.Get("timezone")), FailureChannelID: f.Get("failure_channel")}
	var bad []string
	if _, err := time.LoadLocation(st.Timezone); err != nil || st.Timezone == "" {
		bad = append(bad, "Unknown timezone "+st.Timezone+".")
	}
	num := func(key, label string, lo, hi int) int {
		n, err := strconv.Atoi(f.Get(key))
		if err != nil || n < lo || n > hi {
			bad = append(bad, label+" must be between "+strconv.Itoa(lo)+" and "+strconv.Itoa(hi)+".")
		}
		return n
	}
	st.MaxPlayMinutes = num("max_play_minutes", "Longest playback", 1, 600)
	st.MaxUploadMB = num("max_upload_mb", "Largest upload", 1, 2000)
	st.RunRetentionDays = num("run_retention_days", "Run history", 1, 3650)
	if st.FailureChannelID != "" {
		if ch, _, ok := s.bot.Channel(st.FailureChannelID); !ok || ch.Voice {
			bad = append(bad, "Pick a text channel for failures.")
		}
	}
	if len(bad) > 0 {
		s.fail(w, r, http.StatusBadRequest, strings.Join(bad, " "))
		return
	}
	if err := s.store.SaveSettings(r.Context(), st); err != nil {
		s.internalError(w, r, err)
		return
	}
	SetDisplayTimezone(st.Timezone)
	failure := "none"
	if st.FailureChannelID != "" {
		failure = "#" + s.bot.ChannelName(st.FailureChannelID)
	}
	s.audit(r, "settings.update", "settings", "timezone="+st.Timezone+" max_play="+strconv.Itoa(st.MaxPlayMinutes)+"m max_upload="+strconv.Itoa(st.MaxUploadMB)+"MB failures="+failure+" history="+strconv.Itoa(st.RunRetentionDays)+"d")
	redirect(w, r, "/admin/settings", "saved")
}
