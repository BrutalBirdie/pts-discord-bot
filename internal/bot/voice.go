package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// Playing is a sound that is playing right now.
type Playing struct {
	GuildID   string
	ChannelID string
	Channel   string
	Name      string
	By        string // task name or user
	Started   time.Time
	Duration  time.Duration // 0 when unknown
	cancel    context.CancelFunc
}

func (p Playing) Elapsed() time.Duration { return time.Since(p.Started) }

// VoiceChannelOf returns the voice channel a member is in.
func (b *Bot) VoiceChannelOf(guildID, userID snowflake.ID) (snowflake.ID, bool) {
	c, err := b.get()
	if err != nil {
		return 0, false
	}
	vs, ok := c.Caches.VoiceState(guildID, userID)
	if !ok || vs.ChannelID == nil {
		return 0, false
	}
	return *vs.ChannelID, true
}

// Join connects to a voice channel. A connection to another channel of the
// same guild is closed first.
func (b *Bot) Join(ctx context.Context, guildID, channelID snowflake.ID) error {
	c, err := b.get()
	if err != nil {
		return err
	}
	ch, ok := c.Caches.Channel(channelID)
	if !ok || ch.GuildID() != guildID {
		return &PermanentError{"The voice channel " + channelID.String() + " does not exist in this server."}
	}
	if ch.Type() != discord.ChannelTypeGuildVoice && ch.Type() != discord.ChannelTypeGuildStageVoice {
		return &PermanentError{"#" + ch.Name() + " is not a voice channel."}
	}
	if self, ok := c.Caches.SelfMember(guildID); ok {
		need := discord.PermissionViewChannel | discord.PermissionConnect | discord.PermissionSpeak
		if !c.Caches.MemberPermissionsInChannel(ch, self).Has(need) {
			return &PermanentError{"The bot needs View Channel, Connect and Speak in " + ch.Name() + "."}
		}
	}
	if conn := c.VoiceManager.GetConn(guildID); conn != nil {
		if cur := conn.ChannelID(); cur != nil && *cur == channelID {
			return nil
		}
		closeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		conn.Close(closeCtx)
		cancel()
	}
	conn := c.VoiceManager.CreateConn(guildID)
	openCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := conn.Open(openCtx, channelID, false, true); err != nil {
		conn.Close(context.Background())
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("the voice connection did not open within 15 s")
		}
		return fmt.Errorf("joining voice channel: %w", err)
	}
	return nil
}

// startTimeout bounds the wait for the first frame. disgo pulls frames only
// once the voice gateway and the DAVE session are ready, so a frame pulled
// means audio reaches the channel.
const startTimeout = 15 * time.Second

// stallTimeout ends a playback whose frames stopped being pulled, disgo stops
// pulling while the DAVE session is not ready.
const stallTimeout = 10 * time.Second

func (b *Bot) Leave(ctx context.Context, guildID snowflake.ID) error {
	c, err := b.get()
	if err != nil {
		return err
	}
	b.Stop(guildID)
	if conn := c.VoiceManager.GetConn(guildID); conn != nil {
		closeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		conn.Close(closeCtx)
		cancel()
	}
	return nil
}

// Stop ends the sound playing in a guild, the bot stays in the channel.
func (b *Bot) Stop(guildID snowflake.ID) bool {
	b.vmu.Lock()
	defer b.vmu.Unlock()
	p, ok := b.playing[guildID]
	if ok {
		p.cancel()
	}
	return ok
}

func (b *Bot) NowPlaying() []Playing {
	b.vmu.Lock()
	defer b.vmu.Unlock()
	out := make([]Playing, 0, len(b.playing))
	for _, p := range b.playing {
		out = append(out, *p)
	}
	return out
}

func (b *Bot) playingIn(guildID snowflake.ID) *Playing {
	b.vmu.Lock()
	defer b.vmu.Unlock()
	if p, ok := b.playing[guildID]; ok {
		cp := *p
		return &cp
	}
	return nil
}

var ErrStopped = errors.New("playback was stopped")

// ErrChannelEmpty ends a playback because no member is left in the channel.
var ErrChannelEmpty = errors.New("the voice channel is empty")

// stalled reports whether frames stopped being pulled after the first one.
func stalled(frames int64, last, now time.Time) bool {
	return frames > 0 && now.Sub(last) >= stallTimeout
}

// Play streams a file into the voice channel the bot is in and returns when it
// has finished. volume is in percent. It returns ErrChannelEmpty without
// playing on when no member is in the channel.
func (b *Bot) Play(ctx context.Context, guildID snowflake.ID, path string, info Playing, volume int) error {
	c, err := b.get()
	if err != nil {
		return err
	}
	conn := c.VoiceManager.GetConn(guildID)
	if conn == nil || conn.ChannelID() == nil {
		return errors.New("the bot is not in a voice channel of this server, add a Join step first")
	}
	channelID := *conn.ChannelID()
	info.ChannelID = channelID.String()
	if ch, ok := c.Caches.Channel(channelID); ok {
		info.Channel = ch.Name()
	}
	empty := b.VoiceCond(guildID, tasks.UntilEmpty, 0, channelID)
	wake := b.waiters.add(guildID)
	defer b.waiters.remove(guildID, wake)
	stopEmpty := func() error {
		b.log.Info("stopped playback, channel empty", "guild", guildID, "channel", channelID, "sound", info.Name)
		return ErrChannelEmpty
	}
	if empty() {
		return stopEmpty()
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	info.Started, info.cancel, info.GuildID = time.Now(), cancel, guildID.String()
	b.vmu.Lock()
	if _, busy := b.playing[guildID]; busy {
		b.vmu.Unlock()
		return errors.New("something else is playing in this server")
	}
	b.playing[guildID] = &info
	b.vmu.Unlock()
	defer func() {
		b.vmu.Lock()
		delete(b.playing, guildID)
		b.vmu.Unlock()
	}()

	args := []string{"-hide_banner", "-loglevel", "error", "-i", path, "-vn"}
	if volume > 0 && volume != 100 {
		args = append(args, "-af", "volume="+strconv.FormatFloat(float64(volume)/100, 'f', 2, 64))
	}
	args = append(args, "-c:a", "libopus", "-b:a", "128k", "-ar", "48000", "-ac", "2",
		"-frame_duration", "20", "-application", "audio", "-f", "ogg", "pipe:1")
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("starting ffmpeg: %w", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	provider := newFrameProvider(newOggOpusReader(stdout))
	conn.SetOpusFrameProvider(provider)
	started, startBy := provider.started, time.After(startTimeout)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
wait:
	for {
		select {
		case <-started:
			started, startBy = nil, nil
		case <-startBy:
			provider.Close()
			b.log.Warn("no audio sent", "guild", guildID, "gateway_status", conn.Gateway().Status(), "dave_ready", conn.DAVE().Ready())
			return errors.New("no audio reached Discord within " + startTimeout.String() + ", the voice connection is not ready")
		case <-provider.done:
			break wait
		case <-ctx.Done():
			provider.Close()
			break wait
		case <-wake:
			if empty() {
				provider.Close()
				return stopEmpty()
			}
		case now := <-tick.C:
			if stalled(provider.frames.Load(), provider.lastFrame(), now) {
				provider.Close()
				b.log.Warn("playback stalled", "guild", guildID, "gateway_status", conn.Gateway().Status(), "dave_ready", conn.DAVE().Ready())
				return errors.New("playback stalled, no audio was sent for " + stallTimeout.String())
			}
		}
	}
	// lets the audio sender flush its trailing silence frames
	time.Sleep(200 * time.Millisecond)

	if ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			return errors.New("playback did not finish within the longest playback time")
		}
		return ErrStopped
	}
	if provider.frames.Load() == 0 {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("ffmpeg: %s", msg)
		}
		if provider.err != nil {
			return fmt.Errorf("decoding audio: %w", provider.err)
		}
		return errors.New("no audio frames produced")
	}
	return nil
}

// frameProvider adapts oggOpusReader to voice.OpusFrameProvider. It reports any
// read error as end of stream, because the disgo audio sender would otherwise
// retry a failing provider every 20ms forever.
type frameProvider struct {
	r       *oggOpusReader
	done    chan struct{}
	started chan struct{}
	once    sync.Once
	frames  atomic.Int64
	last    atomic.Int64 // unix nanoseconds of the last frame
	err     error
}

func newFrameProvider(r *oggOpusReader) *frameProvider {
	return &frameProvider{r: r, done: make(chan struct{}), started: make(chan struct{})}
}

func (f *frameProvider) ProvideOpusFrame() ([]byte, error) {
	select {
	case <-f.done:
		return nil, io.EOF
	default:
	}
	frame, err := f.r.NextPacket()
	if err != nil {
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			f.err = err
		}
		f.Close()
		return nil, io.EOF
	}
	f.last.Store(time.Now().UnixNano())
	if f.frames.Add(1) == 1 {
		close(f.started)
	}
	return frame, nil
}

func (f *frameProvider) lastFrame() time.Time { return time.Unix(0, f.last.Load()) }

func (f *frameProvider) Close() {
	f.once.Do(func() { close(f.done) })
}
