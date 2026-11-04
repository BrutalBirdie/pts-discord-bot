package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave/golibdave"
	"github.com/disgoorg/snowflake/v2"
)

type config struct {
	discordToken     string
	apiToken         string
	listenAddr       string
	soundsDir        string
	defaultGuildID   snowflake.ID
	defaultChannelID snowflake.ID
	maxPlayDuration  time.Duration
	maxUploadSize    int64
}

func loadConfig() (config, error) {
	cfg := config{
		discordToken:    os.Getenv("DISCORD_TOKEN"),
		apiToken:        os.Getenv("API_TOKEN"),
		listenAddr:      envOr("LISTEN_ADDR", ":8080"),
		soundsDir:       envOr("SOUNDS_DIR", "/sounds"),
		maxPlayDuration: 10 * time.Minute,
		maxUploadSize:   50 << 20,
	}
	if cfg.discordToken == "" {
		return cfg, errors.New("DISCORD_TOKEN is required")
	}
	if cfg.apiToken == "" {
		return cfg, errors.New("API_TOKEN is required")
	}
	var err error
	if cfg.defaultGuildID, err = optionalID("DEFAULT_GUILD_ID"); err != nil {
		return cfg, err
	}
	if cfg.defaultChannelID, err = optionalID("DEFAULT_CHANNEL_ID"); err != nil {
		return cfg, err
	}
	if v := os.Getenv("MAX_PLAY_DURATION"); v != "" {
		if cfg.maxPlayDuration, err = time.ParseDuration(v); err != nil {
			return cfg, fmt.Errorf("MAX_PLAY_DURATION: %w", err)
		}
	}
	if v := os.Getenv("MAX_UPLOAD_MB"); v != "" {
		mb, err := strconv.ParseInt(v, 10, 64)
		if err != nil || mb <= 0 {
			return cfg, fmt.Errorf("MAX_UPLOAD_MB: must be a positive number, got %q", v)
		}
		cfg.maxUploadSize = mb << 20
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func optionalID(key string) (snowflake.ID, error) {
	v := os.Getenv(key)
	if v == "" {
		return 0, nil
	}
	id, err := snowflake.Parse(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return id, nil
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid config", slog.Any("err", err))
		os.Exit(1)
	}

	client, err := disgo.New(cfg.discordToken,
		bot.WithLogger(logger),
		bot.WithGatewayConfigOpts(gateway.WithIntents(gateway.IntentGuilds, gateway.IntentGuildVoiceStates)),
		bot.WithVoiceManagerConfigOpts(voice.WithDaveSessionCreateFunc(golibdave.NewSession)),
	)
	if err != nil {
		logger.Error("creating discord client", slog.Any("err", err))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err = client.OpenGateway(ctx); err != nil {
		logger.Error("connecting to discord gateway", slog.Any("err", err))
		os.Exit(1)
	}

	p := &player{cfg: cfg, client: client, busy: map[snowflake.ID]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /sounds", p.auth(p.handleSounds))
	mux.HandleFunc("POST /play", p.auth(p.handlePlay))

	srv := &http.Server{Addr: cfg.listenAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		logger.Info("http api listening", slog.String("addr", cfg.listenAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", slog.Any("err", err))
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	client.Close(shutdownCtx)
}

type player struct {
	cfg    config
	client *bot.Client

	mu   sync.Mutex
	busy map[snowflake.ID]bool
}

func (p *player) auth(next http.HandlerFunc) http.HandlerFunc {
	want := []byte("Bearer " + p.cfg.apiToken)
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (p *player) handleSounds(w http.ResponseWriter, _ *http.Request) {
	entries, err := os.ReadDir(p.cfg.soundsDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	files := []string{}
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			files = append(files, e.Name())
		}
	}
	writeJSON(w, http.StatusOK, map[string][]string{"sounds": files})
}

type playRequest struct {
	GuildID   snowflake.ID `json:"guild_id"`
	ChannelID snowflake.ID `json:"channel_id"`
	File      string       `json:"file"`

	path string
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func errStatus(status int, format string, args ...any) error {
	return &httpError{status: status, msg: fmt.Sprintf(format, args...)}
}

// parsePlayRequest accepts three body types:
//   - application/json: {"channel_id", "guild_id", "file"} with file from the sounds directory
//   - multipart/form-data: fields channel_id, guild_id and an uploaded "file" part
//   - anything else: the raw body is the audio, channel_id and guild_id come from the query string
//
// Uploaded audio is written to a temp file; the returned cleanup func removes it.
func (p *player) parsePlayRequest(w http.ResponseWriter, r *http.Request) (playRequest, func(), error) {
	var req playRequest
	noop := func() {}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	r.Body = http.MaxBytesReader(w, r.Body, p.cfg.maxUploadSize)

	switch mediaType {
	case "application/json":
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return req, noop, errStatus(http.StatusBadRequest, "invalid json: %s", err)
		}
		if !filepath.IsLocal(req.File) {
			return req, noop, errStatus(http.StatusBadRequest, "file must be a file name inside the sounds directory")
		}
		req.path = filepath.Join(p.cfg.soundsDir, req.File)
		if fi, err := os.Stat(req.path); err != nil || !fi.Mode().IsRegular() {
			return req, noop, errStatus(http.StatusNotFound, "sound not found: %s", req.File)
		}
		return req, noop, nil

	case "multipart/form-data":
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			return req, noop, errStatus(http.StatusBadRequest, "invalid multipart body: %s", err)
		}
		defer r.MultipartForm.RemoveAll()
		if err := parseIDs(&req, r.FormValue("channel_id"), r.FormValue("guild_id")); err != nil {
			return req, noop, err
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			return req, noop, errStatus(http.StatusBadRequest, "multipart field \"file\" is required")
		}
		defer f.Close()
		req.File = hdr.Filename
		return saveUpload(req, f)

	default:
		if err := parseIDs(&req, r.URL.Query().Get("channel_id"), r.URL.Query().Get("guild_id")); err != nil {
			return req, noop, err
		}
		req.File = "upload"
		return saveUpload(req, r.Body)
	}
}

func parseIDs(req *playRequest, channelID, guildID string) error {
	var err error
	if channelID != "" {
		if req.ChannelID, err = snowflake.Parse(channelID); err != nil {
			return errStatus(http.StatusBadRequest, "invalid channel_id: %s", err)
		}
	}
	if guildID != "" {
		if req.GuildID, err = snowflake.Parse(guildID); err != nil {
			return errStatus(http.StatusBadRequest, "invalid guild_id: %s", err)
		}
	}
	return nil
}

func saveUpload(req playRequest, src io.Reader) (playRequest, func(), error) {
	tmp, err := os.CreateTemp("", "voicebot-upload-*")
	if err != nil {
		return req, func() {}, err
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	n, err := io.Copy(tmp, src)
	_ = tmp.Close()
	if err != nil {
		cleanup()
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return req, func() {}, errStatus(http.StatusRequestEntityTooLarge, "upload larger than %d bytes", tooLarge.Limit)
		}
		return req, func() {}, errStatus(http.StatusBadRequest, "reading upload: %s", err)
	}
	if n == 0 {
		cleanup()
		return req, func() {}, errStatus(http.StatusBadRequest, "empty request body, send the audio file as body")
	}
	req.path = tmp.Name()
	return req, cleanup, nil
}

func (p *player) handlePlay(w http.ResponseWriter, r *http.Request) {
	req, cleanup, err := p.parsePlayRequest(w, r)
	defer cleanup()
	if err != nil {
		status := http.StatusInternalServerError
		var he *httpError
		if errors.As(err, &he) {
			status = he.status
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	if req.ChannelID == 0 {
		req.ChannelID = p.cfg.defaultChannelID
	}
	if req.ChannelID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "channel_id is required (or set DEFAULT_CHANNEL_ID)"})
		return
	}
	if req.GuildID == 0 {
		req.GuildID = p.cfg.defaultGuildID
	}
	if req.GuildID == 0 {
		ch, err := p.client.Rest.GetChannel(req.ChannelID)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "looking up channel: " + err.Error()})
			return
		}
		gc, ok := ch.(discord.GuildChannel)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "channel is not a guild voice channel"})
			return
		}
		req.GuildID = gc.GuildID()
	}

	if !p.acquire(req.GuildID) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already playing in this guild"})
		return
	}
	defer p.release(req.GuildID)

	// Not tied to r.Context(): a client timeout must not leave the bot stuck in the channel.
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.maxPlayDuration)
	defer cancel()

	start := time.Now()
	log := slog.With(slog.String("guild", req.GuildID.String()), slog.String("channel", req.ChannelID.String()), slog.String("file", req.File))
	log.Info("playing")
	if err := p.play(ctx, req.GuildID, req.ChannelID, req.path); err != nil {
		log.Error("playback failed", slog.Any("err", err))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	log.Info("finished", slog.Duration("took", time.Since(start)))
	writeJSON(w, http.StatusOK, map[string]any{"played": req.File, "seconds": time.Since(start).Seconds()})
}

func (p *player) acquire(guildID snowflake.ID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy[guildID] {
		return false
	}
	p.busy[guildID] = true
	return true
}

func (p *player) release(guildID snowflake.ID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.busy, guildID)
}

func (p *player) play(ctx context.Context, guildID, channelID snowflake.ID, path string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-vn",
		"-c:a", "libopus", "-b:a", "128k", "-ar", "48000", "-ac", "2",
		"-frame_duration", "20", "-application", "audio",
		"-f", "ogg", "pipe:1")
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

	conn := p.client.VoiceManager.CreateConn(guildID)
	openCtx, openCancel := context.WithTimeout(ctx, 15*time.Second)
	defer openCancel()
	if err = conn.Open(openCtx, channelID, false, true); err != nil {
		conn.Close(context.Background())
		return fmt.Errorf("joining voice channel: %w", err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		conn.Close(closeCtx)
	}()

	conn.SetOpusFrameProvider(provider)

	select {
	case <-provider.done:
	case <-ctx.Done():
		return fmt.Errorf("playback did not finish within %s", p.cfg.maxPlayDuration)
	}
	// Let the audio sender flush its trailing silence frames before leaving.
	time.Sleep(200 * time.Millisecond)

	if provider.frames == 0 {
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
	r      *oggOpusReader
	done   chan struct{}
	once   sync.Once
	frames int
	err    error
}

func newFrameProvider(r *oggOpusReader) *frameProvider {
	return &frameProvider{r: r, done: make(chan struct{})}
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
	f.frames++
	return frame, nil
}

func (f *frameProvider) Close() {
	f.once.Do(func() { close(f.done) })
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
