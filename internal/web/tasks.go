package web

import (
	"fmt"
	"net/http"
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
	list, err := s.store.ListMedia(r.Context(), "")
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
			name := mediaNames[st.MediaID]
			if name == "" {
				name = "a deleted sound"
			}
			parts = append(parts, "play "+name)
		case tasks.Leave:
			parts = append(parts, "leave")
		case tasks.Message:
			parts = append(parts, "message to #"+s.bot.ChannelName(st.ChannelID))
		case tasks.DM:
			parts = append(parts, "DM to "+s.bot.MemberName(t.GuildID, st.UserID))
		case tasks.Wait:
			parts = append(parts, "wait "+dur(time.Duration(st.Seconds)*time.Second))
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
	Guild string
	What  string
	Next  *time.Time
	Last  *store.Run
}

type tasksData struct {
	Rows   []taskRow
	Filter string
	Query  string
	Counts map[string]int
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.store.ListTasks(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	last, err := s.store.LastRuns(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	names, err := s.mediaNames(r)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	d := tasksData{Filter: r.URL.Query().Get("filter"), Query: strings.TrimSpace(r.URL.Query().Get("q")), Counts: map[string]int{}}
	now := time.Now()
	for _, t := range list {
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
		if d.Query != "" && !strings.Contains(strings.ToLower(t.Name), strings.ToLower(d.Query)) {
			continue
		}
		row := taskRow{Task: t, Guild: s.bot.GuildName(t.GuildID), What: s.describeSteps(t, names)}
		if t.Enabled {
			if next := t.Trigger.Next(now, 1); len(next) == 1 {
				row.Next = &next[0]
			}
		}
		if run, ok := last[t.ID]; ok {
			row.Last = &run
		}
		d.Rows = append(d.Rows, row)
	}
	s.render(w, r, http.StatusOK, "tasks", d)
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
	Sounds    []store.Media
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
}

var commonTimezones = []string{"Europe/Berlin", "Europe/Vienna", "Europe/Zurich", "Europe/London", "Europe/Paris", "Europe/Istanbul",
	"Europe/Moscow", "Asia/Dubai", "Asia/Riyadh", "Asia/Karachi", "Asia/Jakarta", "America/New_York", "America/Chicago", "America/Los_Angeles", "UTC"}

const runsPerPage = 10

func (s *Server) editor(r *http.Request, t tasks.Task, isNew bool) (taskEditor, error) {
	ctx := r.Context()
	e := taskEditor{Task: t, New: isNew, Guilds: s.bot.Guilds(), StepTypes: tasks.StepTypes, Weekdays: tasks.WeekdayNames, Kinds: tasks.TriggerKinds,
		TimesText: strings.Join(t.Trigger.Times, " "), CronText: strings.Join(t.Trigger.Cron, "\n"), Timezones: commonTimezones}
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
	for _, st := range t.Steps {
		for _, id := range []string{st.UserID, st.FollowUserID} {
			if id != "" {
				e.Labels[id] = s.bot.MemberLabel(t.GuildID, id)
			}
		}
	}
	all, err := s.store.ListMedia(ctx, "")
	if err != nil {
		return e, err
	}
	for _, m := range all {
		if media.HasAudio(m) {
			e.Sounds = append(e.Sounds, m)
		}
	}
	if e.Messages, err = s.store.ListMessages(ctx); err != nil {
		return e, err
	}
	e.Next = t.Trigger.Next(time.Now(), 5)
	if !isNew {
		q := store.RunQuery{TaskID: t.ID}
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
	if g, ok := s.bot.Guild(r.URL.Query().Get("guild")); ok {
		t.GuildID = g.ID
	} else if gs := s.bot.Guilds(); len(gs) > 0 {
		t.GuildID = gs[0].ID
	}
	s.renderEditor(w, r, http.StatusOK, t, true, nil, false)
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	t, err := s.store.Task(r.Context(), id)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.renderEditor(w, r, http.StatusOK, t, false, nil, false)
}

func (s *Server) renderEditor(w http.ResponseWriter, r *http.Request, status int, t tasks.Task, isNew bool, problems []string, unsaved bool) {
	e, err := s.editor(r, t, isNew)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	e.Problems, e.Unsaved = problems, unsaved
	s.render(w, r, status, "task", e)
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
			} else {
				st.ChannelID = target
			}
		case tasks.Play:
			st.MediaID, _ = strconv.ParseInt(f.Get(p+"media"), 10, 64)
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
			}
		case tasks.Wait:
			st.Seconds, _ = strconv.Atoi(f.Get(p + "seconds"))
			if f.Get(p+"unit") == "min" {
				st.Seconds *= 60
			}
		case tasks.Leave:
		default:
			continue
		}
		t.Steps = append(t.Steps, st)
	}
	return t, bad
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
			st.Seconds = 5
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
	case "del":
		if i >= 0 && i < len(t.Steps) {
			t.Steps = slices.Delete(t.Steps, i, i+1)
		}
	}
}

// resolveMembers turns the text of the member pickers into user IDs.
func (s *Server) resolveMembers(t *tasks.Task) []string {
	var bad []string
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
	var old tasks.Task
	if !isNew {
		id, ok := pathID(r)
		if !ok {
			s.notFound(w, r)
			return
		}
		var err error
		if old, err = s.store.Task(ctx, id); err != nil {
			s.storeErr(w, r, err)
			return
		}
	}
	t, bad := parseTask(r)
	t.ID, t.PausedReason = old.ID, old.PausedReason
	bad = append(bad, s.resolveMembers(&t)...)
	op := r.PostForm.Get("op")
	if op != "save" {
		applyOp(&t, op)
		s.renderEditor(w, r, http.StatusOK, t, isNew, nil, true)
		return
	}
	if _, ok := s.bot.Guild(t.GuildID); !ok && t.GuildID != old.GuildID {
		bad = append(bad, "Pick a server the bot is in.")
	}
	if problems := append(bad, t.Problems()...); len(problems) > 0 {
		s.renderEditor(w, r, http.StatusUnprocessableEntity, t, isNew, problems, true)
		return
	}
	user := sessionFrom(r).Username
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

func (s *Server) loadTask(w http.ResponseWriter, r *http.Request) (tasks.Task, bool) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return tasks.Task{}, false
	}
	t, err := s.store.Task(r.Context(), id)
	if err != nil {
		s.storeErr(w, r, err)
		return t, false
	}
	return t, true
}

func (s *Server) runTask(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	if problems := t.Problems(); len(problems) > 0 {
		s.fail(w, r, http.StatusBadRequest, "The task cannot run: "+strings.Join(problems, " "))
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
	if r.PostForm.Get("confirm") != t.Name {
		s.fail(w, r, http.StatusBadRequest, "Type the name of the task to delete it.")
		return
	}
	s.run.Unschedule(t.ID)
	if err := s.store.DeleteTask(r.Context(), t.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.audit(r, "task.delete", t.Name, "")
	redirect(w, r, "/tasks", "deleted")
}
