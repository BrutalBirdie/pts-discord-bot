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
	"sync/atomic"
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

const (
	runSlack    = 2 * time.Minute
	maxRunLimit = tasks.MaxWait + time.Hour
	// joinTime and startTime are the bounds of bot.Join and of the first frame in bot.Play.
	joinTime  = 25 * time.Second
	startTime = 15 * time.Second
)

var errRunLimit = errors.New("the run took longer than its estimate")

// runLimit estimates how long a run may take. soundTime returns 0 for a
// sound of unknown length, which counts as maxPlay.
func runLimit(steps []tasks.Step, soundTime func(tasks.SoundRef) time.Duration, maxPlay time.Duration) time.Duration {
	total := runSlack
	for _, s := range steps {
		switch s.Type {
		case tasks.Join:
			total += joinTime
		case tasks.Wait:
			total += s.WaitDuration()
		case tasks.Profile:
			total += 2 * tasks.ProfileMaxWait
		case tasks.Play:
			var play time.Duration
			for _, ref := range s.Sounds() {
				d := soundTime(ref)
				if d <= 0 {
					d = maxPlay
				}
				d += startTime
				if s.Order == tasks.OrderRandom {
					play = max(play, d)
				} else {
					play += d
				}
			}
			total += min(play, maxPlay)
		}
	}
	return min(total, maxRunLimit)
}

// soundTime is the length of a sound for runLimit, 0 when unknown.
func (r *Runner) soundTime(ctx context.Context) func(tasks.SoundRef) time.Duration {
	return func(ref tasks.SoundRef) time.Duration {
		if ref.Tag != "" {
			return 0
		}
		m, err := r.st.Media(ctx, ref.MediaID)
		if err != nil {
			return 0
		}
		return time.Duration(m.PlayMS()) * time.Millisecond
	}
}

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

	onChange atomic.Pointer[func()]

	runsMu  sync.Mutex
	running map[int64]context.CancelCauseFunc // runs in progress, by run ID
	board   map[string]int64                  // the sound played by hand per server, by run ID
}

// runStopped is the cause of a run a user stopped.
type runStopped struct{ by string }

// runReplaced ends a sound played by hand when the next one is played.
var runReplaced = errors.New("replaced by the next sound")

func (e *runStopped) Error() string { return "stopped by " + e.by }

// StopRun ends a run in progress, by is who stopped it. It reports whether the run was in progress.
// OnChange is called when a run starts or ends, for the live web UI.
func (r *Runner) OnChange(f func()) { r.onChange.Store(&f) }

func (r *Runner) changed() {
	if f := r.onChange.Load(); f != nil {
		(*f)()
	}
}

func (r *Runner) StopRun(runID int64, by string) bool {
	r.runsMu.Lock()
	defer r.runsMu.Unlock()
	stop, ok := r.running[runID]
	if ok {
		stop(&runStopped{by: by})
	}
	return ok
}

func New(st *store.Store, b *bot.Bot, dir media.Dir, log *slog.Logger) *Runner {
	return &Runner{st: st, bot: b, media: dir, log: log, entries: map[int64]cron.EntryID{}, locks: map[string]chan struct{}{},
		events: map[int64]tasks.Task{}, cooldown: map[string]time.Time{}, running: map[int64]context.CancelCauseFunc{}, board: map[string]int64{},
		cron: cron.New(cron.WithLocation(time.UTC))}
}

// Start schedules all active tasks. Runs missed while the app was down are not repeated.
func (r *Runner) Start(ctx context.Context) error {
	r.ctx = ctx
	if err := r.st.FailInterruptedRuns(ctx); err != nil {
		return err
	}
	list, err := r.st.ListTasks(ctx, store.All)
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

// PlayNow plays a sound by hand. It cuts short the sound played by hand
// before it and waits for a task that uses voice.
func (r *Runner) PlayNow(guildID, channelID string, m store.Media, volume int, leave bool, actor string) {
	t := PlayTask(guildID, channelID, "Play "+m.Label(), tasks.Step{MediaIDs: []int64{m.ID}, Volume: volume}, leave)
	t.Soundboard, t.Busy = true, tasks.BusyWait
	r.RunNow(t, actor)
}

// soundVolume is the volume of a sound when it has its own, else the volume of the step.
func soundVolume(m store.Media, step int) int {
	if m.Volume > 0 {
		return m.Volume
	}
	return step
}

func clipOf(m store.Media) bot.Clip {
	return bot.Clip{Start: time.Duration(m.TrimStartMS) * time.Millisecond, End: time.Duration(m.TrimEndMS) * time.Millisecond}
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

// SendStepNow runs a single message or direct message step and waits for the result.
func (r *Runner) SendStepNow(guildID string, step tasks.Step, actor, title string) error {
	return r.execute(tasks.Task{Name: title, GuildID: guildID, Steps: []tasks.Step{step}}, store.SourceManual, actor, nil)
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
	r.changed()
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	r.runsMu.Lock()
	r.running[runID] = stop
	if t.Soundboard {
		if prev, ok := r.board[t.GuildID]; ok {
			if stop, ok := r.running[prev]; ok {
				stop(runReplaced)
			}
		}
		r.board[t.GuildID] = runID
	}
	r.runsMu.Unlock()
	defer func() {
		r.runsMu.Lock()
		delete(r.running, runID)
		if r.board[t.GuildID] == runID {
			delete(r.board, t.GuildID)
		}
		r.runsMu.Unlock()
	}()
	stopped := func() (string, bool) {
		var s *runStopped
		if errors.As(context.Cause(ctx), &s) {
			return "Stopped by " + s.by + ".", true
		}
		return "", false
	}
	replaced := func() bool {
		if context.Cause(ctx) != runReplaced {
			return false
		}
		r.finish(log, runID, store.RunDone, nil, "Cut short by the next sound.")
		return true
	}

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
				if replaced() {
					return nil
				}
				if msg, ok := stopped(); ok {
					r.finish(log, runID, store.RunSkipped, nil, msg)
					return nil
				}
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
	limit := runLimit(t.Steps, r.soundTime(ctx), time.Duration(settings.MaxPlayMinutes)*time.Minute)
	rctx, cancel := context.WithTimeoutCause(ctx, limit, errRunLimit)
	defer cancel()
	var notes []string
	for i, s := range t.Steps {
		if replaced() {
			return nil
		}
		if msg, ok := stopped(); ok {
			r.finish(log, runID, store.RunSkipped, nil, fmt.Sprintf("Before step %d: %s", i+1, msg))
			return nil
		}
		if err := r.step(rctx, t, s, settings, ev); err != nil {
			n := i + 1
			if replaced() {
				return nil
			}
			if msg, ok := stopped(); ok {
				r.finish(log, runID, store.RunSkipped, nil, fmt.Sprintf("Step %d: %s", n, msg))
				return nil
			}
			var note *stepNote
			if errors.As(err, &note) {
				log.Info("step note", "step", n, "note", note.msg)
				notes = append(notes, fmt.Sprintf("Step %d: %s", n, note.msg))
				continue
			}
			if context.Cause(rctx) == errRunLimit {
				msg := fmt.Sprintf("The run took longer than its estimated %s, stopped.", tasks.FormatDuration(limit))
				r.finish(log, runID, store.RunFailed, &n, msg)
				if guildID, ok := bot.ParseID(t.GuildID); ok && t.UsesVoice() {
					lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
					if err := r.bot.Leave(lctx, guildID); err != nil {
						log.Error("leaving voice after the run limit", "err", err)
					}
					cancel()
				}
				r.afterFailure(t, n, s, errRunLimit, msg, settings)
				return fmt.Errorf("step %d, %s: %s", n, strings.ToLower(s.Type.Label()), msg)
			}
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
	r.finish(log, runID, store.RunDone, nil, strings.Join(notes, " "))
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
	r.changed()
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

// stepNote lets a run go on after a step that did not do its part, the note
// shows in the run history of the finished run.
type stepNote struct{ msg string }

func (e *stepNote) Error() string { return e.msg }

// stopRun ends a run as skipped, a wait step returns it when its condition timed out.
type stopRun struct{ msg string }

func (e *stopRun) Error() string { return e.msg }

// mediaFile loads a file, gone is the error for a missing one.
func (r *Runner) mediaFile(ctx context.Context, id int64, gone string) (store.Media, error) {
	m, err := r.st.Media(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Media{}, &bot.PermanentError{Msg: gone}
	}
	return m, err
}

// sound loads one entry of a play step in guildID, a tag entry as a random sound with the tag offered there.
func (r *Runner) sound(ctx context.Context, guildID string, ref tasks.SoundRef) (store.Media, error) {
	if ref.Tag != "" {
		m, err := r.st.RandomSound(ctx, guildID, ref.Tag)
		if errors.Is(err, store.ErrNotFound) {
			return m, &bot.PermanentError{Msg: "No sound has the tag " + ref.Tag + "."}
		}
		return m, err
	}
	return r.mediaFile(ctx, ref.MediaID, "A sound was deleted from the media library.")
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
			m, err := r.sound(ctx, t.GuildID, ref)
			if err != nil {
				return err
			}
			list = append(list, m)
		}
		pctx, cancel := context.WithTimeout(ctx, time.Duration(settings.MaxPlayMinutes)*time.Minute)
		defer cancel()
		for _, m := range list {
			info := bot.Playing{Name: m.Label(), By: t.Name, Duration: time.Duration(m.PlayMS()) * time.Millisecond}
			err := r.bot.Play(pctx, guildID, r.media.Path(m.ID), clipOf(m), info, soundVolume(m, s.Volume))
			if errors.Is(err, bot.ErrChannelEmpty) {
				return &stepNote{"nothing played, nobody else was in the voice channel."}
			}
			if err != nil {
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
		m, err := r.mediaFile(ctx, s.AvatarMediaID, "The avatar image was deleted from the media library.")
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
// The files must be in the scope of the message, whoever saved the task.
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
		md, err := r.mediaFile(ctx, id, fmt.Sprintf("File %d was deleted from the media library.", id))
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
	mc.Components = buttonRows(m.ID, m.Buttons)
	if total > messages.MaxRequestBytes {
		closeAll()
		return mc, nil, &bot.PermanentError{Msg: fmt.Sprintf("The files have %d MB together, Discord accepts at most 25 MB per message.", total>>20)}
	}
	if mc.Content == "" && len(mc.Embeds) == 0 && len(mc.Files) == 0 && len(mc.Components) == 0 {
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

// ButtonID is the custom ID of a button: the task it starts, the saved message and the button's index there.
func ButtonID(taskID, messageID int64, i int) string {
	return fmt.Sprintf("task:%d:%d:%d", taskID, messageID, i)
}

// ParseButtonID reads a custom ID made by ButtonID.
func ParseButtonID(id string) (taskID, messageID int64, i int, ok bool) {
	n, err := fmt.Sscanf(id, "task:%d:%d:%d", &taskID, &messageID, &i)
	return taskID, messageID, i, err == nil && n == 3 && taskID > 0
}

var buttonStyles = map[string]discord.ButtonStyle{messages.StylePrimary: discord.ButtonStylePrimary, messages.StyleSecondary: discord.ButtonStyleSecondary,
	messages.StyleSuccess: discord.ButtonStyleSuccess, messages.StyleDanger: discord.ButtonStyleDanger}

// buttonRows lays out the buttons of a message in rows of five.
func buttonRows(messageID int64, list []messages.Button) []discord.LayoutComponent {
	var rows []discord.LayoutComponent
	var row []discord.InteractiveComponent
	for i, b := range list {
		var c discord.ButtonComponent
		if b.IsLink() {
			c = discord.NewLinkButton(b.Label, b.URL)
		} else {
			style, ok := buttonStyles[b.Style]
			if !ok {
				style = discord.ButtonStylePrimary
			}
			c = discord.NewButton(style, b.Label, ButtonID(b.TaskID, messageID, i), "", 0)
		}
		if b.Emoji != "" {
			c = c.WithEmoji(discord.NewComponentEmoji(b.Emoji))
		}
		row = append(row, c)
		if len(row) == messages.ButtonsPerRow || i == len(list)-1 {
			rows = append(rows, discord.NewActionRow(row...))
			row = nil
		}
	}
	return rows
}
