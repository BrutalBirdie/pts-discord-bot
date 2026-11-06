// Package runner schedules tasks and executes their steps.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"github.com/robfig/cron/v3"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/media"
	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// busyWait is how long a run waits for another voice run in the same server.
const busyWait = 30 * time.Minute

type Runner struct {
	st    *store.Store
	bot   *bot.Bot
	media media.Dir
	log   *slog.Logger
	ctx   context.Context

	mu      sync.Mutex
	cron    *cron.Cron
	entries map[int64]cron.EntryID
	events  map[int64]tasks.Task // active tasks with an event trigger

	coolMu   sync.Mutex
	cooldown map[string]time.Time // task and member to the time the task may start again

	locksMu sync.Mutex
	locks   map[string]chan struct{}
	wg      sync.WaitGroup
}

func New(st *store.Store, b *bot.Bot, dir media.Dir, log *slog.Logger) *Runner {
	return &Runner{st: st, bot: b, media: dir, log: log, entries: map[int64]cron.EntryID{}, locks: map[string]chan struct{}{},
		events: map[int64]tasks.Task{}, cooldown: map[string]time.Time{},
		cron: cron.New(cron.WithLocation(time.UTC))}
}

// Start schedules all active tasks. Runs missed while the app was down are not repeated.
func (r *Runner) Start(ctx context.Context) error {
	r.ctx = ctx
	if err := r.st.FailInterruptedRuns(ctx); err != nil {
		return err
	}
	list, err := r.st.ListTasks(ctx)
	if err != nil {
		return err
	}
	r.recordMissed(ctx, list)
	for _, t := range list {
		r.Schedule(t)
	}
	go r.heartbeat(ctx)
	r.cron.Start()
	go func() {
		<-ctx.Done()
		<-r.cron.Stop().Done()
	}()
	return nil
}

// Wait blocks until running tasks have finished after the context ended.
func (r *Runner) Wait() { r.wg.Wait() }

// Schedule adds, replaces or removes the cron entry of a task.
func (r *Runner) Schedule(t tasks.Task) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.entries[t.ID]; ok {
		r.cron.Remove(id)
		delete(r.entries, t.ID)
	}
	delete(r.events, t.ID)
	if !t.Enabled {
		return
	}
	if t.Trigger.Kind == tasks.Event {
		r.events[t.ID] = t
		return
	}
	sched, err := t.Trigger.Schedule()
	if err != nil {
		r.log.Error("task has an invalid schedule", "task", t.ID, "err", err)
		return
	}
	if sched == nil {
		return
	}
	taskID := t.ID
	r.entries[t.ID] = r.cron.Schedule(sched, cron.FuncJob(func() { r.fire(taskID) }))
}

func (r *Runner) Unschedule(taskID int64) { r.Schedule(tasks.Task{ID: taskID}) }

func (r *Runner) fire(taskID int64) {
	t, err := r.st.Task(r.ctx, taskID)
	if err != nil {
		r.log.Error("scheduled task not loaded", "task", taskID, "err", err)
		return
	}
	if !t.Enabled {
		return
	}
	r.wg.Add(1)
	defer r.wg.Done()
	r.execute(t, store.SourceSchedule, "", nil)
}

// heartbeat lets the next start find the runs missed while the app was down.
func (r *Runner) heartbeat(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := r.st.SetHeartbeat(ctx, time.Now()); err != nil && ctx.Err() == nil {
			r.log.Error("writing heartbeat", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// recordMissed lists the scheduled runs between the last heartbeat and now as skipped.
func (r *Runner) recordMissed(ctx context.Context, list []tasks.Task) {
	last, err := r.st.Heartbeat(ctx)
	if err != nil || last.IsZero() {
		return
	}
	now := time.Now()
	for _, t := range list {
		if !t.Enabled {
			continue
		}
		for _, at := range t.Trigger.Next(last, 50) {
			if !at.Before(now) {
				break
			}
			if err := r.st.AddMissedRun(ctx, t.ID, t.Name, t.GuildID, at); err != nil {
				r.log.Error("recording missed run", "task", t.ID, "err", err)
				return
			}
			r.log.Warn("run missed while the app was down", "task", t.Name, "at", at)
		}
	}
}

// OnEvent starts the active tasks whose event trigger matches. A task starts
// once per member within its cooldown.
func (r *Runner) OnEvent(ev bot.Event) {
	r.mu.Lock()
	var match []tasks.Task
	for _, t := range r.events {
		if t.GuildID == ev.GuildID && t.Trigger.Matches(ev.Type, ev.ChannelID, ev.UserID, ev.Content) {
			match = append(match, t)
		}
	}
	r.mu.Unlock()
	now := time.Now()
	for _, t := range match {
		key := strconv.FormatInt(t.ID, 10) + ":" + ev.UserID
		r.coolMu.Lock()
		if until, ok := r.cooldown[key]; ok && now.Before(until) {
			r.coolMu.Unlock()
			continue
		}
		if len(r.cooldown) > 10000 {
			for k, until := range r.cooldown {
				if now.After(until) {
					delete(r.cooldown, k)
				}
			}
		}
		r.cooldown[key] = now.Add(time.Duration(t.Trigger.Cooldown) * time.Second)
		r.coolMu.Unlock()
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.execute(t, store.SourceEvent, ev.UserName, &ev)
		}()
	}
}

// RunNow starts a task in the background.
func (r *Runner) RunNow(t tasks.Task, actor string) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.execute(t, store.SourceManual, actor, nil)
	}()
}

// RunFromDiscord starts a task from a slash command, ev is the member who used it.
func (r *Runner) RunFromDiscord(t tasks.Task, ev bot.Event) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.execute(t, store.SourceDiscord, ev.UserName, &ev)
	}()
}

// PlayNow joins a voice channel and plays a file in the background.
// PlayTask is a task that plays a sound for a sound, or for a random sound with the tag.
func PlayTask(guildID, channelID, title string, play tasks.Step, leave bool) tasks.Task {
	play.Type = tasks.Play
	if play.Volume == 0 {
		play.Volume = 100
	}
	steps := []tasks.Step{{Type: tasks.Join, ChannelID: channelID}, play}
	if leave {
		steps = append(steps, tasks.Step{Type: tasks.Leave})
	}
	return tasks.Task{Name: title, GuildID: guildID, Busy: tasks.BusySkip, Steps: steps}
}

func (r *Runner) PlayNow(guildID, channelID string, m store.Media, leave bool, actor string) {
	r.RunNow(PlayTask(guildID, channelID, "Play "+m.Name, tasks.Step{MediaIDs: []int64{m.ID}}, leave), actor)
}

// JoinNow connects the bot to a voice channel in the background.
func (r *Runner) JoinNow(guildID, channelID, actor string) {
	r.RunNow(tasks.Task{Name: "Join voice channel", GuildID: guildID, Busy: tasks.BusySkip, Steps: []tasks.Step{{Type: tasks.Join, ChannelID: channelID}}}, actor)
}

// SendNow sends a saved message and waits for the result.
func (r *Runner) SendNow(guildID, channelID string, messageID int64, actor string, title string) error {
	t := tasks.Task{Name: title, GuildID: guildID, Steps: []tasks.Step{{Type: tasks.Message, ChannelID: channelID, MessageID: messageID}}}
	return r.execute(t, store.SourceManual, actor, nil)
}

func (r *Runner) lock(guildID string) chan struct{} {
	r.locksMu.Lock()
	defer r.locksMu.Unlock()
	l, ok := r.locks[guildID]
	if !ok {
		l = make(chan struct{}, 1)
		r.locks[guildID] = l
	}
	return l
}

func (r *Runner) execute(t tasks.Task, source, actor string, ev *bot.Event) error {
	ctx := r.ctx
	run := store.Run{Title: t.Name, GuildID: t.GuildID, Source: source, Actor: actor}
	if t.ID != 0 {
		run.TaskID = &t.ID
	}
	runID, err := r.st.StartRun(ctx, run)
	if err != nil {
		r.log.Error("recording run", "task", t.Name, "err", err)
		return err
	}
	log := r.log.With("run", runID, "task", t.Name, "source", source)
	log.Info("run started")

	if t.UsesVoice() {
		l := r.lock(t.GuildID)
		select {
		case l <- struct{}{}:
		default:
			wait := busyWait
			if t.Busy == tasks.BusySkip {
				wait = 0
			}
			select {
			case l <- struct{}{}:
			case <-time.After(wait):
				r.finish(log, runID, store.RunSkipped, nil, "The bot was busy with voice in this server.")
				return errors.New("the bot is busy with voice in this server")
			case <-ctx.Done():
				r.finish(log, runID, store.RunFailed, nil, "The app stopped.")
				return ctx.Err()
			}
		}
		defer func() { <-l }()
	}

	settings, err := r.st.Settings(ctx)
	if err != nil {
		settings = store.DefaultSettings
	}
	for i, s := range t.Steps {
		if err := r.step(ctx, t, s, settings, ev); err != nil {
			n := i + 1
			var stop *stopRun
			if errors.As(err, &stop) {
				r.finish(log, runID, store.RunSkipped, nil, fmt.Sprintf("Step %d: %s", n, stop.msg))
				return nil
			}
			msg := bot.Describe(err)
			r.finish(log, runID, store.RunFailed, &n, msg)
			r.afterFailure(t, n, s, err, msg, settings)
			return fmt.Errorf("step %d, %s: %s", n, strings.ToLower(s.Type.Label()), msg)
		}
	}
	r.finish(log, runID, store.RunDone, nil, "")
	if t.ID != 0 && t.Trigger.Kind == tasks.Once && source == store.SourceSchedule {
		if err := r.st.SetTaskEnabled(context.WithoutCancel(ctx), t.ID, false, "", "system"); err != nil {
			log.Error("pausing one-off task", "err", err)
		}
		r.Unschedule(t.ID)
	}
	return nil
}

func (r *Runner) finish(log *slog.Logger, runID int64, status string, failedStep *int, errText string) {
	if status == store.RunDone {
		log.Info("run finished")
	} else {
		args := []any{"err", errText}
		if failedStep != nil {
			args = append(args, "step", *failedStep)
		}
		log.Warn("run "+status, args...)
	}
	if err := r.st.FinishRun(context.WithoutCancel(r.ctx), runID, status, failedStep, errText); err != nil {
		log.Error("recording run result", "err", err)
	}
}

func (r *Runner) afterFailure(t tasks.Task, n int, s tasks.Step, err error, msg string, settings store.Settings) {
	ctx := context.WithoutCancel(r.ctx)
	if t.ID != 0 && bot.Permanent(err) {
		reason := fmt.Sprintf("Step %d, %s: %s", n, strings.ToLower(s.Type.Label()), msg)
		if err := r.st.SetTaskEnabled(ctx, t.ID, false, reason, "system"); err != nil {
			r.log.Error("pausing task", "task", t.ID, "err", err)
		}
		_ = r.st.Audit(ctx, store.AuditEntry{Actor: "system", Action: "task.pause", Target: t.Name, Detail: reason})
		r.Unschedule(t.ID)
	}
	if id, ok := bot.ParseID(settings.FailureChannelID); ok && r.ctx.Err() == nil {
		text := fmt.Sprintf("Task **%s** failed at step %d (%s): %s", t.Name, n, strings.ToLower(s.Type.Label()), msg)
		if err := r.bot.Send(id, discord.MessageCreate{Content: truncate(text, messages.MaxContent), AllowedMentions: &discord.AllowedMentions{}}); err != nil {
			r.log.Error("posting failure", "err", err)
		}
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// stopRun ends a run as skipped, a wait step returns it when its condition timed out.
type stopRun struct{ msg string }

func (e *stopRun) Error() string { return e.msg }

// sound loads one entry of a play step, a tag entry as a random sound with the tag.
func (r *Runner) sound(ctx context.Context, ref tasks.SoundRef) (store.Media, error) {
	if ref.Tag != "" {
		m, err := r.st.RandomSound(ctx, ref.Tag)
		if errors.Is(err, store.ErrNotFound) {
			return m, &bot.PermanentError{Msg: "No sound has the tag " + ref.Tag + "."}
		}
		return m, err
	}
	m, err := r.st.Media(ctx, ref.MediaID)
	if errors.Is(err, store.ErrNotFound) {
		return m, &bot.PermanentError{Msg: "A sound was deleted from the media library."}
	}
	return m, err
}

func (r *Runner) step(ctx context.Context, t tasks.Task, s tasks.Step, settings store.Settings, ev *bot.Event) error {
	if s.ToTrigger {
		if ev == nil || ev.UserID == "" {
			return &bot.PermanentError{Msg: "The step uses the member who started the run, but this run was not started by a member."}
		}
		s.FollowUserID, s.UserID = ev.UserID, ev.UserID
	}
	guildID, ok := bot.ParseID(t.GuildID)
	if !ok {
		return &bot.PermanentError{Msg: "The task has no server."}
	}
	switch s.Type {
	case tasks.Join:
		channelID, ok := bot.ParseID(s.ChannelID)
		if s.FollowUserID != "" {
			uid, _ := bot.ParseID(s.FollowUserID)
			if channelID, ok = r.bot.VoiceChannelOf(guildID, uid); !ok {
				return fmt.Errorf("%s is not in a voice channel", r.bot.MemberName(t.GuildID, s.FollowUserID))
			}
		}
		if !ok {
			return &bot.PermanentError{Msg: "No voice channel picked."}
		}
		return r.bot.Join(ctx, guildID, channelID)

	case tasks.Play:
		refs := s.Sounds()
		if len(refs) == 0 {
			return &bot.PermanentError{Msg: "No sound picked."}
		}
		if s.Order == tasks.OrderRandom {
			refs = []tasks.SoundRef{refs[rand.IntN(len(refs))]}
		}
		// resolved before playing, so a deleted sound does not cut a sequence in half
		list := make([]store.Media, 0, len(refs))
		for _, ref := range refs {
			m, err := r.sound(ctx, ref)
			if err != nil {
				return err
			}
			list = append(list, m)
		}
		pctx, cancel := context.WithTimeout(ctx, time.Duration(settings.MaxPlayMinutes)*time.Minute)
		defer cancel()
		for _, m := range list {
			info := bot.Playing{Name: m.Name, By: t.Name, Duration: time.Duration(m.DurationMS) * time.Millisecond}
			if err := r.bot.Play(pctx, guildID, r.media.Path(m.ID), info, s.Volume); err != nil {
				return err
			}
		}
		return nil

	case tasks.Leave:
		return r.bot.Leave(ctx, guildID)

	case tasks.Wait:
		d := s.WaitDuration()
		if s.Until == "" {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
		userID, _ := bot.ParseID(s.UserID)
		channelID, _ := bot.ParseID(s.ChannelID)
		if tasks.MemberUntil(s.Until) && userID == 0 {
			return &bot.PermanentError{Msg: "No member picked."}
		}
		wctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		if r.bot.WaitVoice(wctx, guildID, r.bot.VoiceCond(guildID, s.Until, userID, channelID)) == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.OnTimeout == tasks.OnTimeoutStop {
			what := s.UntilText(r.bot.MemberName(t.GuildID, s.UserID), r.bot.ChannelName(s.ChannelID))
			return &stopRun{fmt.Sprintf("Waited %s %s, the run stopped.", tasks.FormatDuration(d), what)}
		}
		return nil

	case tasks.Message, tasks.DM:
		msg := messages.Message{Content: s.Text}
		if s.MessageID != 0 {
			var err error
			if msg, err = r.st.Message(ctx, s.MessageID); errors.Is(err, store.ErrNotFound) {
				return &bot.PermanentError{Msg: "The saved message was deleted."}
			} else if err != nil {
				return err
			}
		}
		vars := r.vars(t, settings, ev)
		if s.Type == tasks.Message {
			vars.Channel = "#" + r.bot.ChannelName(s.ChannelID)
		}
		mc, closeFiles, err := r.Build(ctx, msg, vars)
		if err != nil {
			return err
		}
		defer closeFiles()
		if s.Type == tasks.DM {
			uid, ok := bot.ParseID(s.UserID)
			if !ok {
				return &bot.PermanentError{Msg: "No member picked."}
			}
			return r.bot.SendDM(uid, mc)
		}
		cid, ok := bot.ParseID(s.ChannelID)
		if !ok {
			return &bot.PermanentError{Msg: "No text channel picked."}
		}
		return r.bot.Send(cid, mc)

	case tasks.Profile:
		return r.profile(ctx, t, s, settings, ev, guildID)
	}
	return &bot.PermanentError{Msg: fmt.Sprintf("Unknown step %q.", s.Type)}
}

func location(t tasks.Task, settings store.Settings) *time.Location {
	loc, err := t.Trigger.Location()
	if err != nil || t.Trigger.Timezone == "" {
		loc, _ = time.LoadLocation(settings.Timezone)
	}
	if loc == nil {
		loc = time.UTC
	}
	return loc
}

func (r *Runner) vars(t tasks.Task, settings store.Settings, ev *bot.Event) messages.Vars {
	vars := messages.Vars{Now: time.Now().In(location(t, settings)), Server: r.bot.GuildName(t.GuildID)}
	if ev != nil {
		vars.MemberID, vars.MemberName = ev.UserID, ev.UserName
	}
	return vars
}

// profile changes the bot's nickname and avatar in the server of the task. It
// waits for a short rate limit and skips the run for a longer one.
func (r *Runner) profile(ctx context.Context, t tasks.Task, s tasks.Step, settings store.Settings, ev *bot.Event, guildID snowflake.ID) error {
	fields := s.ProfileFields()
	if len(fields) == 0 {
		return &bot.PermanentError{Msg: "Nothing to change picked."}
	}
	var p bot.ProfileChange
	switch {
	case s.ResetNick:
		p.Nick = new(string)
	case s.Nick != "":
		vars := r.vars(t, settings, ev)
		// a mention would show as <@id> in a nickname
		vars.MemberID = ""
		nick := truncate(strings.TrimSpace(messages.Expand(s.Nick, vars)), tasks.MaxNick)
		if nick == "" {
			return &bot.PermanentError{Msg: "The nickname is empty after filling in the placeholders."}
		}
		p.Nick = &nick
	}
	switch {
	case s.ResetAvatar:
		p.ResetAvatar = true
	case s.AvatarMediaID != 0:
		m, err := r.st.Media(ctx, s.AvatarMediaID)
		if errors.Is(err, store.ErrNotFound) {
			return &bot.PermanentError{Msg: "The avatar image was deleted from the media library."}
		}
		if err != nil {
			return err
		}
		if msg := media.AvatarProblem(m); msg != "" {
			return &bot.PermanentError{Msg: msg + "."}
		}
		data, err := os.ReadFile(r.media.Path(m.ID))
		if err != nil {
			return err
		}
		typ := media.AvatarType(data)
		if typ == "" {
			return &bot.PermanentError{Msg: m.Name + " is not a PNG, JPEG, GIF or WebP image."}
		}
		p.Avatar = discord.NewIconRaw(discord.IconType(typ), data)
	}

	limits, err := r.st.ProfileLimits(ctx, t.GuildID)
	if err != nil {
		return err
	}
	loc := location(t, settings)
	skipped := func(at time.Time) error {
		return &stopRun{"Profile change skipped, Discord allows the next change at " + at.In(loc).Format("15:04") + "."}
	}
	wait, at, skip := tasks.ProfileDecide(time.Now(), limits, fields)
	if skip {
		return skipped(at)
	}
	if wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	cctx, cancel := context.WithTimeout(ctx, tasks.ProfileMaxWait)
	defer cancel()
	err = r.bot.UpdateProfile(cctx, guildID, p)
	var rl *bot.RateLimitedError
	if errors.As(err, &rl) {
		if err := r.st.BlockProfile(context.WithoutCancel(ctx), t.GuildID, fields, rl.Until); err != nil {
			r.log.Error("recording profile rate limit", "err", err)
		}
		return skipped(rl.Until)
	}
	if err != nil {
		return err
	}
	if err := r.st.RecordProfileChange(context.WithoutCancel(ctx), t.GuildID, fields, time.Now()); err != nil {
		r.log.Error("recording profile change", "err", err)
	}
	return nil
}

// Build turns a saved message into a Discord message with its files opened.
func (r *Runner) Build(ctx context.Context, m messages.Message, vars messages.Vars) (discord.MessageCreate, func(), error) {
	var opened []io.Closer
	closeAll := func() {
		for _, c := range opened {
			_ = c.Close()
		}
	}
	mc := discord.MessageCreate{Content: messages.Expand(m.Content, vars)}
	attached := map[int64]string{}
	var total int64
	attach := func(id int64) (string, error) {
		if name, ok := attached[id]; ok {
			return name, nil
		}
		md, err := r.st.Media(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return "", &bot.PermanentError{Msg: fmt.Sprintf("File %d was deleted from the media library.", id)}
		}
		if err != nil {
			return "", err
		}
		f, err := os.Open(r.media.Path(id))
		if err != nil {
			return "", err
		}
		opened = append(opened, f)
		total += md.SizeBytes
		mc.Files = append(mc.Files, discord.NewFile(md.Name, "", f))
		attached[id] = md.Name
		return md.Name, nil
	}
	for _, id := range m.Files {
		if _, err := attach(id); err != nil {
			closeAll()
			return mc, nil, err
		}
	}
	for _, e := range m.Embeds {
		de := discord.Embed{Title: messages.Expand(e.Title, vars), URL: e.URL, Description: messages.Expand(e.Description, vars), Color: e.ColorInt()}
		if e.Footer != "" {
			de.Footer = &discord.EmbedFooter{Text: messages.Expand(e.Footer, vars)}
		}
		for _, f := range e.Fields {
			inline := f.Inline
			de.Fields = append(de.Fields, discord.EmbedField{Name: messages.Expand(f.Name, vars), Value: messages.Expand(f.Value, vars), Inline: &inline})
		}
		for _, img := range []struct {
			id  int64
			set func(string)
		}{
			{e.ImageMediaID, func(n string) { de.Image = &discord.EmbedResource{URL: "attachment://" + n} }},
			{e.ThumbMediaID, func(n string) { de.Thumbnail = &discord.EmbedResource{URL: "attachment://" + n} }},
		} {
			if img.id == 0 {
				continue
			}
			name, err := attach(img.id)
			if err != nil {
				closeAll()
				return mc, nil, err
			}
			img.set(name)
		}
		mc.Embeds = append(mc.Embeds, de)
	}
	if total > messages.MaxRequestBytes {
		closeAll()
		return mc, nil, &bot.PermanentError{Msg: fmt.Sprintf("The files have %d MB together, Discord accepts at most 25 MB per message.", total>>20)}
	}
	if mc.Content == "" && len(mc.Embeds) == 0 && len(mc.Files) == 0 {
		closeAll()
		return mc, nil, &bot.PermanentError{Msg: "The message is empty."}
	}
	return mc, closeAll, nil
}

// GuildOf finds the guild of a channel for one-off sends.
func (r *Runner) GuildOf(channelID string) (string, bool) {
	_, gid, ok := r.bot.Channel(channelID)
	return gid, ok
}
