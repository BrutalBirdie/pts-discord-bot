// Package runner schedules tasks and executes their steps.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
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

	locksMu sync.Mutex
	locks   map[string]chan struct{}
	wg      sync.WaitGroup
}

func New(st *store.Store, b *bot.Bot, dir media.Dir, log *slog.Logger) *Runner {
	return &Runner{st: st, bot: b, media: dir, log: log, entries: map[int64]cron.EntryID{}, locks: map[string]chan struct{}{},
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
	for _, t := range list {
		r.Schedule(t)
	}
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
	if !t.Enabled {
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
	r.execute(t, store.SourceSchedule, "")
}

// RunNow starts a task in the background.
func (r *Runner) RunNow(t tasks.Task, actor string) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.execute(t, store.SourceManual, actor)
	}()
}

// PlayNow joins a voice channel and plays a file in the background.
func (r *Runner) PlayNow(guildID, channelID string, m store.Media, leave bool, actor string) {
	steps := []tasks.Step{{Type: tasks.Join, ChannelID: channelID}, {Type: tasks.Play, MediaID: m.ID, Volume: 100}}
	if leave {
		steps = append(steps, tasks.Step{Type: tasks.Leave})
	}
	r.RunNow(tasks.Task{Name: "Play " + m.Name, GuildID: guildID, Busy: tasks.BusySkip, Steps: steps}, actor)
}

// SendNow sends a saved message and waits for the result.
func (r *Runner) SendNow(guildID, channelID string, messageID int64, actor string, title string) error {
	t := tasks.Task{Name: title, GuildID: guildID, Steps: []tasks.Step{{Type: tasks.Message, ChannelID: channelID, MessageID: messageID}}}
	return r.execute(t, store.SourceManual, actor)
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

func (r *Runner) execute(t tasks.Task, source, actor string) error {
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
		if err := r.step(ctx, t, s, settings); err != nil {
			n := i + 1
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
		log.Warn("run "+status, "step", failedStep, "err", errText)
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

func (r *Runner) step(ctx context.Context, t tasks.Task, s tasks.Step, settings store.Settings) error {
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
		m, err := r.st.Media(ctx, s.MediaID)
		if errors.Is(err, store.ErrNotFound) {
			return &bot.PermanentError{Msg: "The sound was deleted from the media library."}
		}
		if err != nil {
			return err
		}
		pctx, cancel := context.WithTimeout(ctx, time.Duration(settings.MaxPlayMinutes)*time.Minute)
		defer cancel()
		info := bot.Playing{Name: m.Name, By: t.Name, Duration: time.Duration(m.DurationMS) * time.Millisecond}
		return r.bot.Play(pctx, guildID, r.media.Path(m.ID), info, s.Volume)

	case tasks.Leave:
		return r.bot.Leave(ctx, guildID)

	case tasks.Wait:
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(s.Seconds) * time.Second):
			return nil
		}

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
		loc, err := t.Trigger.Location()
		if err != nil || t.Trigger.Timezone == "" {
			loc, _ = time.LoadLocation(settings.Timezone)
		}
		if loc == nil {
			loc = time.UTC
		}
		vars := messages.Vars{Now: time.Now().In(loc), Server: r.bot.GuildName(t.GuildID)}
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
	}
	return &bot.PermanentError{Msg: fmt.Sprintf("Unknown step %q.", s.Type)}
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
