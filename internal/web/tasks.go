package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/media"
	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

func (s *Server) mediaNames(r *http.Request) (map[int64]string, error) {
	list, err := s.store.ListMedia(r.Context(), "", "")
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(list))
	for _, m := range list {
		out[m.ID] = m.Name
	}
	return out, nil
}

// describeSteps is the one line summary of what a task does.
func (s *Server) describeSteps(t tasks.Task, mediaNames map[int64]string) string {
	parts := make([]string, 0, len(t.Steps))
	for _, st := range t.Steps {
		switch st.Type {
		case tasks.Join:
			if st.FollowUserID != "" {
				parts = append(parts, "join "+s.bot.MemberName(t.GuildID, st.FollowUserID))
			} else {
				parts = append(parts, "join "+s.bot.ChannelName(st.ChannelID))
			}
		case tasks.Play:
			var names []string
			for _, ref := range st.Sounds() {
				name := mediaNames[ref.MediaID]
				if ref.Tag != "" {
					name = "a random sound tagged " + ref.Tag
				} else if name == "" {
					name = "a deleted sound"
				}
				names = append(names, name)
			}
			sep := " then "
			if st.Order == tasks.OrderRandom {
				sep = " or "
			}
			parts = append(parts, "play "+strings.Join(names, sep))
		case tasks.Leave:
			parts = append(parts, "leave")
		case tasks.Message:
			parts = append(parts, "message to #"+s.bot.ChannelName(st.ChannelID))
		case tasks.DM:
			parts = append(parts, "DM to "+s.bot.MemberName(t.GuildID, st.UserID))
		case tasks.Wait:
			if st.Until == "" {
				parts = append(parts, "wait "+tasks.FormatDuration(st.WaitDuration()))
			} else {
				parts = append(parts, "wait "+st.UntilText(s.bot.MemberName(t.GuildID, st.UserID), s.bot.ChannelName(st.ChannelID))+" (max "+tasks.FormatDuration(st.WaitDuration())+")")
			}
		case tasks.Profile:
			name := mediaNames[st.AvatarMediaID]
			if name == "" {
				name = "a deleted image"
			}
			parts = append(parts, st.ProfileText(name))
		}
	}
	out := strings.Join(parts, ", ")
	if out == "" {
		return "no steps"
	}
	return strings.ToUpper(out[:1]) + out[1:]
}

type taskRow struct {
	Task  tasks.Task
	Guild string // the server's name, on lists across servers
	What  string
	Next  *time.Time
	Last  *store.Run
	Run   int64 // the run in progress
}

// taskRows adds what the task rows show: the steps in words, the next and the last run.
func (s *Server) taskRows(r *http.Request, list []tasks.Task, guildNames bool) ([]taskRow, error) {
	last, err := s.store.LastRuns(r.Context())
	if err != nil {
		return nil, err
	}
	names, err := s.mediaNames(r)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	rows := make([]taskRow, 0, len(list))
	for _, t := range list {
		row := taskRow{Task: t, What: s.describeSteps(t, names)}
		if guildNames {
			row.Guild = s.bot.GuildName(t.GuildID)
		}
		if t.Enabled {
			if next := t.Trigger.Next(now, 1); len(next) == 1 {
				row.Next = &next[0]
			}
		}
		if run, ok := last[t.ID]; ok {
			row.Last = &run
			if run.Status == store.RunRunning {
				row.Run = run.ID
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

type tasksData struct {
	Guild  bot.Guild // the open server, empty without one
	Rows   []taskRow
	Filter string
	Counts map[string]int
	Paging paging
}

// tasks lists the tasks of a server. /tasks opens the user's first server,
// and lists all tasks when the bot knows no server, like while it is offline.
func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := tasksData{Filter: r.URL.Query().Get("filter"), Counts: map[string]int{}, Paging: newPaging(r)}
	if r.PathValue("id") == "" {
		if g, ok := s.serverOf(r); ok {
			http.Redirect(w, r, withQuery("/room/"+g.ID+"/tasks", r.URL.RawQuery), http.StatusSeeOther)
			return
		}
		r = at(r, place{Section: "tasks"})
	} else {
		g, ok := s.bot.Guild(r.PathValue("id"))
		if !ok || !sessionFrom(r).CanGuild(g.ID) {
			s.notFound(w, r)
			return
		}
		d.Guild = g
		r = at(r, place{Guild: g.ID, Section: "tasks", Members: true})
	}
	list, err := s.store.ListTasks(ctx, sessionFrom(r).Scope())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	d.Paging.Keep("filter", d.Filter)
	var shown []tasks.Task
	for _, t := range list {
		if d.Guild.ID != "" && t.GuildID != d.Guild.ID {
			continue
		}
		state := "paused"
		if t.Enabled {
			state = "active"
		}
		d.Counts[""]++
		d.Counts[state]++
		if t.Trigger.Kind == tasks.Manual {
			d.Counts["manual"]++
		}
		if d.Filter != "" && d.Filter != state && !(d.Filter == "manual" && t.Trigger.Kind == tasks.Manual) {
			continue
		}
		if d.Paging.Matches(t.Name) {
			shown = append(shown, t)
		}
	}
	if d.Rows, err = s.taskRows(r, window(&d.Paging, shown), d.Guild.ID == ""); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "tasks", d)
}

// withQuery adds a raw query to a path.
func withQuery(path, query string) string {
	if query == "" {
		return path
	}
	return path + "?" + query
}

type taskEditor struct {
	Task      tasks.Task
	New       bool
	Problems  []string
	Unsaved   bool
	Guilds    []bot.Guild
	Voice     []bot.Channel
	Text      []bot.Channel
	Labels    map[string]string // member picker text per user ID
	EvMembers []bot.Member      // the members an event trigger is limited to
	Sounds    []store.Media
	Images    []store.Media // avatars of profile steps
	Messages  []messages.Message
	Next      []time.Time
	Runs      []store.Run
	RunCount  int
	Page      int
	Pages     int
	TimesText string
	CronText  string
	Timezones []string
	StepTypes []tasks.StepType
	Weekdays  []string
	Kinds     []tasks.TriggerKind
	Events    []string
	Tags      []string
	Overlaps  []overlapView // event tasks that start on the same events

	MaxPlayMinutes int // longest playback of a play step
}

var commonTimezones = []string{"Europe/Berlin", "Europe/Vienna", "Europe/Zurich", "Europe/London", "Europe/Paris", "Europe/Istanbul",
	"Europe/Moscow", "Asia/Dubai", "Asia/Riyadh", "Asia/Karachi", "Asia/Jakarta", "America/New_York", "America/Chicago", "America/Los_Angeles", "UTC"}

const runsPerPage = 10

func (s *Server) editor(r *http.Request, t tasks.Task, isNew bool) (taskEditor, error) {
	ctx := r.Context()
	e := taskEditor{Task: t, New: isNew, Guilds: s.guilds(r), StepTypes: tasks.StepTypes, Weekdays: tasks.WeekdayNames, Kinds: tasks.TriggerKinds,
		TimesText: strings.Join(t.Trigger.Times, " "), CronText: strings.Join(t.Trigger.Cron, "\n"), Timezones: commonTimezones, Events: tasks.EventTypes}
	for _, c := range s.bot.Channels(t.GuildID) {
		if !c.Usable {
			continue
		}
		if c.Voice {
			e.Voice = append(e.Voice, c)
		} else {
			e.Text = append(e.Text, c)
		}
	}
	e.Labels = map[string]string{}
	for _, id := range t.Trigger.UserIDs {
		m, ok := s.bot.ResolveMember(t.GuildID, id)
		if !ok {
			m = bot.Member{ID: id, Name: id}
		}
		e.EvMembers = append(e.EvMembers, m)
	}
	for _, st := range t.Steps {
		for _, id := range []string{st.UserID, st.FollowUserID} {
			if id != "" {
				e.Labels[id] = s.bot.MemberLabel(t.GuildID, id)
			}
		}
	}
	all, msgs, err := s.taskItems(ctx, t)
	if err != nil {
		return e, err
	}
	e.Messages = msgs
	for _, m := range all {
		if media.HasAudio(m) {
			e.Sounds = append(e.Sounds, m)
		}
		if m.Kind == store.KindImage {
			e.Images = append(e.Images, m)
		}
	}
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return e, err
	}
	e.MaxPlayMinutes = settings.MaxPlayMinutes
	if e.Tags, err = s.store.Tags(ctx, t.GuildID); err != nil {
		return e, err
	}
	if t.Trigger.Kind == tasks.Event && t.GuildID != "" {
		list, err := s.store.ListTasks(ctx, store.GuildScope(t.GuildID))
		if err != nil {
			return e, err
		}
		for _, o := range tasks.EventOverlaps(t, list) {
			v := overlapView{Task: o.Task, Fix: len(t.Trigger.UserIDs) == 0 || t.Trigger.Except}
			for _, id := range o.UserIDs {
				m, ok := s.bot.ResolveMember(t.GuildID, id)
				if !ok {
					m = bot.Member{ID: id, Name: id}
				}
				v.Members = append(v.Members, m)
			}
			e.Overlaps = append(e.Overlaps, v)
		}
	}
	e.Next = t.Trigger.Next(time.Now(), 5)
	if !isNew {
		q := store.RunQuery{Scope: sessionFrom(r).Scope(), TaskID: t.ID}
		if e.RunCount, err = s.store.CountRuns(ctx, q); err != nil {
			return e, err
		}
		e.Pages = max(1, (e.RunCount+runsPerPage-1)/runsPerPage)
		e.Page, _ = strconv.Atoi(r.URL.Query().Get("page"))
		e.Page = min(max(e.Page, 1), e.Pages)
		q.Limit, q.Offset = runsPerPage, (e.Page-1)*runsPerPage
		if e.Runs, err = s.store.ListRuns(ctx, q); err != nil {
			return e, err
		}
	}
	return e, nil
}

func (s *Server) newTask(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.Settings(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	t := tasks.Task{Enabled: true, Busy: tasks.BusyWait, Trigger: tasks.Trigger{Kind: tasks.Daily, Timezone: settings.Timezone}}
	t.GuildID = s.defaultGuild(r, r.URL.Query().Get("guild"))
	prefill(&t, r.URL.Query(), s.bot.Members(t.GuildID))
	s.renderEditor(w, r, http.StatusOK, t, true, nil, false)
}

// prefill starts a new task from the Control Room: ?join a voice channel,
// ?play a sound, ?post in a text channel, or greet the ?member who joins voice.
func prefill(t *tasks.Task, q url.Values, members []bot.Member) {
	if m := q.Get("member"); m != "" {
		t.Trigger = tasks.Trigger{Kind: tasks.Event, Event: tasks.VoiceJoin, Timezone: t.Trigger.Timezone, UserIDs: []string{m}, Cooldown: tasks.DefaultCooldown}
		t.Steps = append(t.Steps, tasks.Step{Type: tasks.Join, ToTrigger: true})
		if found, ok := bot.MatchMember(members, m); ok && found.Username != "" {
			t.Name = "Greet " + found.Name
		}
	}
	if c := q.Get("join"); c != "" {
		t.Steps = append(t.Steps, tasks.Step{Type: tasks.Join, ChannelID: c})
	}
	if id, err := strconv.ParseInt(q.Get("play"), 10, 64); err == nil && id > 0 {
		t.Steps = append(t.Steps, tasks.Step{Type: tasks.Play, MediaIDs: []int64{id}, Volume: 100})
	}
	if c := q.Get("post"); c != "" {
		t.Steps = append(t.Steps, tasks.Step{Type: tasks.Message, ChannelID: c})
	}
}

// defaultGuild is the server preselected in a new task or message: the wanted
// one if the user may use it, else the first they may use.
func (s *Server) defaultGuild(r *http.Request, want string) string {
	sess := sessionFrom(r)
	if _, ok := s.bot.Guild(want); ok && sess.CanGuild(want) {
		return want
	}
	if gs := s.guilds(r); len(gs) > 0 {
		return gs[0].ID
	}
	if len(sess.Granted) > 0 {
		return sess.Granted[0]
	}
	return ""
}

// guilds are the servers of the bot the user may manage.
func (s *Server) guilds(r *http.Request) []bot.Guild {
	sess := sessionFrom(r)
	gs := slices.DeleteFunc(s.bot.Guilds(), func(g bot.Guild) bool { return !sess.CanGuild(g.ID) })
	if s.store == nil {
		return gs
	}
	prefs, err := s.store.Prefs(r.Context(), sess.Username)
	if err != nil {
		s.log.Error("reading the server order", "err", err)
		return gs
	}
	return orderGuilds(gs, prefs.GuildOrder)
}

// orderGuilds sorts servers in the user's order, servers they did not
// place yet keep their name order after them.
func orderGuilds(gs []bot.Guild, order []string) []bot.Guild {
	rank := func(id string) int {
		if i := slices.Index(order, id); i >= 0 {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(gs, func(a, b bot.Guild) int { return rank(a.ID) - rank(b.ID) })
	return gs
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	if t, ok := s.loadTask(w, r); ok {
		s.renderEditor(w, r, http.StatusOK, t, false, nil, false)
	}
}

func (s *Server) renderEditor(w http.ResponseWriter, r *http.Request, status int, t tasks.Task, isNew bool, problems []string, unsaved bool) {
	e, err := s.editor(r, t, isNew)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	e.Problems, e.Unsaved = problems, unsaved
	s.render(w, at(r, place{Guild: t.GuildID, Section: "tasks"}), status, "task", e)
}

// parseTask reads the editor form. Fields that do not parse are kept empty,
// Problems reports them on save.
func parseTask(r *http.Request) (tasks.Task, []string) {
	f := r.PostForm
	var bad []string
	t := tasks.Task{
		Name:    strings.TrimSpace(f.Get("name")),
		GuildID: f.Get("guild"),
		Enabled: f.Get("enabled") == "on",
		Slash:   f.Get("slash") == "on",
		Busy:    tasks.BusyWait,
		Trigger: tasks.Trigger{Kind: tasks.TriggerKind(f.Get("kind")), Timezone: strings.TrimSpace(f.Get("timezone")), OnceAt: f.Get("once_at")},
	}
	if f.Get("busy") == tasks.BusySkip {
		t.Busy = tasks.BusySkip
	}
	if !slices.Contains(tasks.TriggerKinds, t.Trigger.Kind) {
		t.Trigger.Kind = tasks.Daily
	}
	times, err := tasks.ParseTimes(f.Get("times"))
	if err != nil {
		if t.Trigger.Kind == tasks.Daily || t.Trigger.Kind == tasks.Weekly {
			bad = append(bad, "Times: "+err.Error()+".")
		}
	}
	t.Trigger.Times = times
	for _, v := range f["weekday"] {
		if d, err := strconv.Atoi(v); err == nil && d >= 0 && d < 7 && !slices.Contains(t.Trigger.Weekdays, d) {
			t.Trigger.Weekdays = append(t.Trigger.Weekdays, d)
		}
	}
	slices.Sort(t.Trigger.Weekdays)
	for _, line := range strings.Split(f.Get("cron"), "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			t.Trigger.Cron = append(t.Trigger.Cron, line)
		}
	}
	// keep only the fields of the selected kind
	switch t.Trigger.Kind {
	case tasks.Daily:
		t.Trigger.Weekdays, t.Trigger.Cron, t.Trigger.OnceAt = nil, nil, ""
	case tasks.Weekly:
		t.Trigger.Cron, t.Trigger.OnceAt = nil, ""
	case tasks.Once:
		t.Trigger.Times, t.Trigger.Weekdays, t.Trigger.Cron = nil, nil, nil
	case tasks.Cron:
		t.Trigger.Times, t.Trigger.Weekdays, t.Trigger.OnceAt = nil, nil, ""
	case tasks.Manual:
		t.Trigger = tasks.Trigger{Kind: tasks.Manual, Timezone: t.Trigger.Timezone}
	case tasks.Event:
		ev := tasks.Trigger{Kind: tasks.Event, Timezone: t.Trigger.Timezone, Event: f.Get("event"), ChannelID: f.Get("event_channel"), Cooldown: tasks.DefaultCooldown}
		if ev.Event == tasks.MessageIn {
			ev.Keyword = strings.TrimSpace(f.Get("keyword"))
		}
		if ev.Event == tasks.MemberJoin {
			ev.ChannelID = ""
		}
		ev.Except = f.Get("ev_except") == "on"
		// the member picker texts, resolved to IDs in resolveMembers
		for _, v := range f["ev_member"] {
			if v = strings.TrimSpace(v); v != "" && !slices.Contains(ev.UserIDs, v) {
				ev.UserIDs = append(ev.UserIDs, v)
			}
		}
		if v := strings.TrimSpace(f.Get("cooldown")); v != "" {
			if ev.Cooldown, err = tasks.ParseCooldown(v); err != nil {
				ev.Cooldown = tasks.DefaultCooldown
				bad = append(bad, "Cooldown: "+err.Error()+".")
			}
		}
		t.Trigger = ev
	}

	n, _ := strconv.Atoi(f.Get("steps"))
	n = min(max(n, 0), tasks.MaxSteps)
	for i := range n {
		p := "s" + strconv.Itoa(i) + "."
		st := tasks.Step{Type: tasks.StepType(f.Get(p + "type"))}
		switch st.Type {
		case tasks.Join:
			// the member picker text, resolved to an ID in resolveMembers
			if target := f.Get(p + "target"); target == "member" {
				st.FollowUserID = strings.TrimSpace(f.Get(p + "follow"))
			} else if target == "trigger" {
				st.ToTrigger = true
			} else {
				st.ChannelID = target
			}
		case tasks.Play:
			for _, v := range f[p+"media"] {
				if tag, ok := strings.CutPrefix(v, "tag:"); ok {
					st.Tag = tag
				} else if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
					st.MediaIDs = append(st.MediaIDs, id)
				}
			}
			// the search text of the sound picker, resolved in resolveSounds
			st.Find = strings.TrimSpace(f.Get(p + "find"))
			if f.Get(p+"order") == tasks.OrderRandom {
				st.Order = tasks.OrderRandom
			}
			st.Volume, err = strconv.Atoi(f.Get(p + "volume"))
			if err != nil {
				st.Volume = 100
			}
		case tasks.Message, tasks.DM:
			st.ChannelID = f.Get(p + "channel")
			st.UserID = strings.TrimSpace(f.Get(p + "user"))
			st.MessageID, _ = strconv.ParseInt(f.Get(p+"message"), 10, 64)
			if st.MessageID == 0 {
				st.Text = strings.TrimSpace(f.Get(p + "text"))
			}
			if st.Type == tasks.Message {
				st.UserID = ""
			} else {
				st.ChannelID = ""
				if f.Get(p+"to") == "trigger" {
					st.ToTrigger, st.UserID = true, ""
				}
			}
		case tasks.Wait:
			parseWait(&st, f, p)
		case tasks.Profile:
			switch f.Get(p + "nick_mode") {
			case "set":
				if st.Nick = strings.TrimSpace(f.Get(p + "nick")); st.Nick == "" {
					bad = append(bad, fmt.Sprintf("Step %d: enter the nickname.", i+1))
				}
			case "reset":
				st.ResetNick = true
			}
			switch v := f.Get(p + "avatar"); v {
			case "":
			case "reset":
				st.ResetAvatar = true
			default:
				st.AvatarMediaID, _ = strconv.ParseInt(v, 10, 64)
			}
		case tasks.Leave:
		default:
			continue
		}
		t.Steps = append(t.Steps, st)
	}
	return t, bad
}

const defaultWait = 5 * time.Second

func parseWait(st *tasks.Step, f url.Values, p string) {
	st.Until = f.Get(p + "until")
	if st.Until != "" {
		if f.Get(p+"on_timeout") == tasks.OnTimeoutStop {
			st.OnTimeout = tasks.OnTimeoutStop
		}
		if tasks.MemberUntil(st.Until) {
			if f.Get(p+"to") == "trigger" {
				st.ToTrigger = true
			} else {
				// the member picker text, resolved to an ID in resolveMembers
				st.UserID = strings.TrimSpace(f.Get(p + "user"))
			}
		}
		if st.Until != tasks.UntilUndeafened && st.Until != tasks.UntilUnmuted {
			st.ChannelID = f.Get(p + "channel")
		}
	}
	in := strings.TrimSpace(f.Get(p + "wait"))
	if in == "" && st.Until != "" {
		st.WaitMS = tasks.DefaultUntilWait.Milliseconds()
		return
	}
	d, err := tasks.ParseDuration(in)
	if err != nil {
		st.WaitInput = in
		return
	}
	// picking a condition turns the time only default into the condition default
	if st.Until != "" && f.Get(p+"until_was") == "" && d == defaultWait {
		d = tasks.DefaultUntilWait
	}
	st.WaitMS = d.Milliseconds()
}

// applyOp changes the steps for the editor buttons that do not save.
func applyOp(t *tasks.Task, op string) {
	verb, arg, _ := strings.Cut(op, ":")
	i, _ := strconv.Atoi(arg)
	switch verb {
	case "add":
		st := tasks.Step{Type: tasks.StepType(arg)}
		switch st.Type {
		case tasks.Play:
			st.Volume = 100
		case tasks.Wait:
			st.WaitMS = defaultWait.Milliseconds()
		}
		if slices.Contains(tasks.StepTypes, st.Type) && len(t.Steps) < tasks.MaxSteps {
			t.Steps = append(t.Steps, st)
		}
	case "up":
		if i > 0 && i < len(t.Steps) {
			t.Steps[i-1], t.Steps[i] = t.Steps[i], t.Steps[i-1]
		}
	case "down":
		if i >= 0 && i < len(t.Steps)-1 {
			t.Steps[i+1], t.Steps[i] = t.Steps[i], t.Steps[i+1]
		}
	case "tokdel":
		removeToken(t, arg)
	case "except":
		tr := &t.Trigger
		if (len(tr.UserIDs) == 0 || tr.Except) && arg != "" && !slices.Contains(tr.UserIDs, arg) && len(tr.UserIDs) < tasks.MaxEventMembers {
			tr.Except = true
			tr.UserIDs = append(tr.UserIDs, arg)
			for i := range tr.UserIDs {
				tr.UserIDs[i] = strings.TrimSpace(tr.UserIDs[i])
			}
		}
	case "del":
		if i >= 0 && i < len(t.Steps) {
			t.Steps = slices.Delete(t.Steps, i, i+1)
		}
	}
}

// resolveMembers turns the text of the member pickers into user IDs.
func (s *Server) resolveMembers(t *tasks.Task) []string {
	var bad []string
	var ids []string
	for _, v := range t.Trigger.UserIDs {
		m, ok := s.bot.ResolveMember(t.GuildID, v)
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("Event: no member of this server matches %q. Pick one from the suggestions.", v))
			ids = append(ids, v)
		case !slices.Contains(ids, m.ID):
			ids = append(ids, m.ID)
		}
	}
	t.Trigger.UserIDs = ids
	for i := range t.Steps {
		for _, field := range []*string{&t.Steps[i].UserID, &t.Steps[i].FollowUserID} {
			if *field == "" {
				continue
			}
			if m, ok := s.bot.ResolveMember(t.GuildID, *field); ok {
				*field = m.ID
			} else {
				bad = append(bad, fmt.Sprintf("Step %d: no member of this server matches %q. Pick one from the suggestions.", i+1, *field))
			}
		}
	}
	return bad
}

func (s *Server) postTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	isNew := r.PathValue("id") == ""
	sess := sessionFrom(r)
	var old tasks.Task
	if !isNew {
		var ok bool
		if old, ok = s.loadTask(w, r); !ok {
			return
		}
	}
	t, bad := parseTask(r)
	if sess.Discord() && !sess.CanGuild(t.GuildID) {
		s.notFound(w, r)
		return
	}
	t.ID, t.PausedReason = old.ID, old.PausedReason
	bad = append(bad, s.resolveMembers(&t)...)
	badSounds, err := s.resolveSounds(ctx, &t)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	bad = append(bad, badSounds...)
	op := r.PostForm.Get("op")
	if op != "save" {
		applyOp(&t, op)
		s.renderEditor(w, r, http.StatusOK, t, isNew, nil, true)
		return
	}
	if _, ok := s.bot.Guild(t.GuildID); !ok && t.GuildID != old.GuildID {
		bad = append(bad, "Pick a server the bot is in.")
	}
	sounds, msgs, err := s.taskItems(ctx, t)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	bad = append(bad, avatarProblems(t, sounds)...)
	sounds = slices.DeleteFunc(sounds, func(m store.Media) bool { return !media.HasAudio(m) })
	bad = append(bad, playProblems(t, sounds, settings.MaxPlayMinutes)...)
	bad = append(bad, messageProblems(t, sounds, msgs)...)
	if problems := append(bad, t.Problems()...); len(problems) > 0 {
		s.renderEditor(w, r, http.StatusUnprocessableEntity, t, isNew, problems, true)
		return
	}
	user := sess.Username
	if isNew {
		id, err := s.store.CreateTask(ctx, t, user)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		t.ID = id
		s.audit(r, "task.create", t.Name, t.Trigger.Summary())
	} else {
		if err := s.store.UpdateTask(ctx, t, user); err != nil {
			s.storeErr(w, r, err)
			return
		}
		s.audit(r, "task.update", t.Name, t.Trigger.Summary())
	}
	s.run.Schedule(t)
	redirect(w, r, fmt.Sprintf("/tasks/%d", t.ID), "saved")
}

// taskItems are the files and messages the editor of t offers: those offered
// to its server, its own first, and those its steps picked.
func (s *Server) taskItems(ctx context.Context, t tasks.Task) ([]store.Media, []messages.Message, error) {
	list, err := s.store.ListMedia(ctx, t.GuildID, "")
	if err != nil {
		return nil, nil, err
	}
	msgs, err := s.store.ListMessages(ctx, t.GuildID)
	if err != nil {
		return nil, nil, err
	}
	var ids []int64
	for _, st := range t.Steps {
		for _, id := range append(slices.Clone(st.MediaIDs), st.AvatarMediaID) {
			if id != 0 && !slices.ContainsFunc(list, func(m store.Media) bool { return m.ID == id }) {
				ids = append(ids, id)
			}
		}
		if st.MessageID != 0 && (st.Type == tasks.Message || st.Type == tasks.DM) && !slices.ContainsFunc(msgs, func(m messages.Message) bool { return m.ID == st.MessageID }) {
			m, err := s.store.Message(ctx, st.MessageID)
			if err == nil {
				msgs = append(msgs, m)
			} else if !errors.Is(err, store.ErrNotFound) {
				return nil, nil, err
			}
		}
	}
	picked, err := s.store.MediaByIDs(ctx, ids)
	return append(list, picked...), msgs, err
}

// messageProblems checks that the saved messages and tagged sounds of the steps exist.
func messageProblems(t tasks.Task, sounds []store.Media, msgs []messages.Message) []string {
	var out []string
	for i, st := range t.Steps {
		if (st.Type == tasks.Message || st.Type == tasks.DM) && st.MessageID != 0 &&
			!slices.ContainsFunc(msgs, func(m messages.Message) bool { return m.ID == st.MessageID }) {
			out = append(out, fmt.Sprintf("Step %d: the saved message was deleted, pick another.", i+1))
		}
		if st.Type == tasks.Play && st.Tag != "" && !slices.ContainsFunc(sounds, func(m store.Media) bool { return slices.Contains(m.Tags, st.Tag) }) {
			out = append(out, fmt.Sprintf("Step %d: no sound available to this server has the tag %s.", i+1, st.Tag))
		}
	}
	return out
}

// loadTask answers not found for tasks of servers the user may not manage.
func (s *Server) loadTask(w http.ResponseWriter, r *http.Request) (tasks.Task, bool) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return tasks.Task{}, false
	}
	t, err := s.store.Task(r.Context(), id)
	if err == nil && !sessionFrom(r).CanGuild(t.GuildID) {
		err = store.ErrNotFound
	}
	if err != nil {
		s.storeErr(w, r, err)
		return t, false
	}
	return t, true
}

// stopRun ends a run in progress, from the run lists.
func (s *Server) stopRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	sess := sessionFrom(r)
	run, err := s.store.Run(r.Context(), id)
	if err == nil && !sess.Scope().Has(run.GuildID) {
		err = store.ErrNotFound
	}
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	by := sess.DisplayName
	if by == "" {
		by = sess.Username
	}
	if !s.run.StopRun(run.ID, by) {
		s.fail(w, r, http.StatusConflict, "The run has finished already.")
		return
	}
	s.audit(r, "run.stop", run.Title, "")
	redirect(w, r, safeNext(r.PostForm.Get("next"), "/"), "stopped-run")
}

const runNeedsMember = "A step uses the member who started the run, so only its event or /task can start it."

func (s *Server) runTask(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	if problems := t.Problems(); len(problems) > 0 {
		s.fail(w, r, http.StatusBadRequest, "The task cannot run: "+strings.Join(problems, " "))
		return
	}
	if t.NeedsMember() {
		s.fail(w, r, http.StatusBadRequest, runNeedsMember)
		return
	}
	s.run.RunNow(t, sessionFrom(r).Username)
	s.audit(r, "task.run", t.Name, "")
	redirect(w, r, safeNext(r.PostForm.Get("next"), fmt.Sprintf("/tasks/%d", t.ID)), "started")
}

func (s *Server) setEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	if err := s.store.SetTaskEnabled(r.Context(), t.ID, enabled, "", sessionFrom(r).Username); err != nil {
		s.internalError(w, r, err)
		return
	}
	t.Enabled = enabled
	s.run.Schedule(t)
	action, done := "task.pause", "paused"
	if enabled {
		action, done = "task.resume", "resumed"
	}
	s.audit(r, action, t.Name, "")
	redirect(w, r, safeNext(r.PostForm.Get("next"), fmt.Sprintf("/tasks/%d", t.ID)), done)
}

func (s *Server) pauseTask(w http.ResponseWriter, r *http.Request)  { s.setEnabled(w, r, false) }
func (s *Server) resumeTask(w http.ResponseWriter, r *http.Request) { s.setEnabled(w, r, true) }

func (s *Server) duplicateTask(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	t.Name += " (copy)"
	t.Enabled = false
	id, err := s.store.CreateTask(r.Context(), t, sessionFrom(r).Username)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r, "task.create", t.Name, "copy")
	redirect(w, r, fmt.Sprintf("/tasks/%d", id), "copied")
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	s.run.Unschedule(t.ID)
	trashed, err := s.removeItem(r, store.TrashTask, t.ID)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.audit(r, "task.delete", t.Name, trashDetail(trashed))
	removed(w, r, "/tasks", store.TrashTask, t.ID, trashed)
}

// avatarProblems checks the avatar images of profile steps against the library and Discord's limits.
func avatarProblems(t tasks.Task, all []store.Media) []string {
	var out []string
	byID := soundsByID(all)
	for i, st := range t.Steps {
		if st.Type != tasks.Profile || st.AvatarMediaID == 0 {
			continue
		}
		m, ok := byID[st.AvatarMediaID]
		if !ok {
			out = append(out, fmt.Sprintf("Step %d: the avatar image was deleted from the media library, pick another.", i+1))
		} else if msg := media.AvatarProblem(m); msg != "" {
			out = append(out, fmt.Sprintf("Step %d: %s.", i+1, msg))
		}
	}
	return out
}

// avatarView is the image picker of a profile step.
type avatarView struct {
	Images  []store.Media // usable as avatar
	Picked  string        // name of a picked image that is not in Images
	Problem string
}

// overlapView is another event task that starts with this one, for the
// members listed or nearly every member when Members is empty. Fix offers to
// leave the listed members out of this task.
type overlapView struct {
	Task    tasks.Task
	Members []bot.Member
	Fix     bool
}

// MissingNick tells whether the bot cannot change its nickname in the task's server.
func (e taskEditor) MissingNick() bool {
	for _, g := range e.Guilds {
		if g.ID == e.Task.GuildID {
			return g.MissingNick()
		}
	}
	return false
}

// NoJoinBefore tells whether no Join step comes before step i.
func (e taskEditor) NoJoinBefore(i int) bool {
	for _, st := range e.Task.Steps[:min(i, len(e.Task.Steps))] {
		if st.Type == tasks.Join {
			return false
		}
	}
	return true
}

// CooldownText is the cooldown of an event trigger as typed, like 30s or 2m.
func (e taskEditor) CooldownText() string {
	n := tasks.DefaultCooldown
	if e.Task.Trigger.Kind == tasks.Event {
		n = e.Task.Trigger.Cooldown
	}
	if n == 0 {
		return "0"
	}
	return tasks.FormatDuration(time.Duration(n) * time.Second)
}

func (e taskEditor) Avatar(i int) avatarView {
	st := e.Task.Steps[i]
	var v avatarView
	for _, m := range e.Images {
		if msg := media.AvatarProblem(m); msg == "" {
			v.Images = append(v.Images, m)
		} else if m.ID == st.AvatarMediaID {
			v.Picked, v.Problem = m.Name, msg
		}
	}
	if st.AvatarMediaID != 0 && v.Picked == "" && !slices.ContainsFunc(v.Images, func(m store.Media) bool { return m.ID == st.AvatarMediaID }) {
		v.Picked, v.Problem = "Deleted image", "The image was deleted from the media library"
	}
	return v
}
