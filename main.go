package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave/golibdave"

	"github.com/BrutalBirdie/pts-discord-bot/internal/access"
	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/commands"
	"github.com/BrutalBirdie/pts-discord-bot/internal/media"
	"github.com/BrutalBirdie/pts-discord-bot/internal/runner"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/web"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	for _, key := range []string{"CLOUDRON_APP_ORIGIN", "CLOUDRON_POSTGRESQL_URL", "CLOUDRON_OIDC_ISSUER", "CLOUDRON_OIDC_CLIENT_ID", "CLOUDRON_OIDC_CLIENT_SECRET"} {
		if os.Getenv(key) == "" {
			return errors.New(key + " is not set, the app needs the postgresql and oidc addons")
		}
	}

	st, err := store.Open(ctx, os.Getenv("CLOUDRON_POSTGRESQL_URL"))
	if err != nil {
		return err
	}
	defer st.Close()
	for deadline := time.Now().Add(60 * time.Second); ; {
		if err = st.CreateSchema(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return err
		}
		log.Warn("database not ready", "err", err)
		time.Sleep(2 * time.Second)
	}

	dir := media.Dir(env("MEDIA_DIR", "/app/data/media"))
	if err := os.MkdirAll(string(dir), 0o750); err != nil {
		return err
	}
	if err := importSounds(ctx, st, dir, env("SOUNDS_DIR", "/app/data/sounds"), log); err != nil {
		log.Error("importing old sounds", "err", err)
	}

	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	web.SetDisplayTimezone(settings.Timezone)

	b := bot.New(bot.Config{
		Token:          os.Getenv("DISCORD_TOKEN"),
		MembersIntent:  os.Getenv("BOT_MEMBERS_INTENT") != "false",
		MessageIntent:  os.Getenv("BOT_MESSAGE_CONTENT_INTENT") != "false",
		PresenceIntent: os.Getenv("BOT_PRESENCE_INTENT") != "false",
		Voice:          []voice.ManagerConfigOpt{voice.WithDaveSessionCreateFunc(golibdave.NewSession)},
		Logger:         log,
	})
	rn := runner.New(st, b, dir, log)
	if err := rn.Start(ctx); err != nil {
		return err
	}
	b.SetHandler(commands.New(st, b, rn, log))
	botDone := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(botDone)
	}()

	srv, err := web.New(web.Config{
		Origin:        os.Getenv("CLOUDRON_APP_ORIGIN"),
		Issuer:        os.Getenv("CLOUDRON_OIDC_ISSUER"),
		ClientID:      os.Getenv("CLOUDRON_OIDC_CLIENT_ID"),
		ClientSecret:  os.Getenv("CLOUDRON_OIDC_CLIENT_SECRET"),
		Admins:        access.Parse(os.Getenv("BOT_ADMINS"), os.Getenv("BOT_ADMIN_GROUP")),
		MediaDir:      dir,
		DiscordSecret: os.Getenv("DISCORD_CLIENT_SECRET"),
	}, st, b, rn, log)
	if err != nil {
		return err
	}
	go srv.PurgeLoop(ctx)

	addr := env("LISTEN_ADDR", ":8000")
	hs := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	hs.RegisterOnShutdown(srv.StopLive)
	go func() {
		log.Info("web ui listening", "addr", addr)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = hs.Shutdown(shutdownCtx)
	waited := make(chan struct{})
	go func() {
		rn.Wait()
		<-botDone
		close(waited)
	}()
	select {
	case <-waited:
	case <-shutdownCtx.Done():
	}
	return nil
}

// importSounds moves the files of the old /app/data/sounds directory into the media library.
func importSounds(ctx context.Context, st *store.Store, dir media.Dir, soundsDir string, log *slog.Logger) error {
	entries, err := os.ReadDir(soundsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(soundsDir, e.Name())
		name, err := media.CleanName(e.Name())
		if err != nil {
			continue
		}
		if _, err := st.MediaByName(ctx, name); err == nil {
			continue
		}
		m, err := media.Probe(ctx, path, name)
		if err != nil {
			return err
		}
		m.UploadedBy = "import"
		id, err := st.AddMedia(ctx, m)
		if err != nil {
			return err
		}
		if err := os.Rename(path, dir.Path(id)); err != nil {
			_ = st.DeleteMedia(ctx, id)
			return err
		}
		log.Info("imported sound into the media library", "name", name)
	}
	if rest, err := os.ReadDir(soundsDir); err == nil && len(rest) == 0 {
		_ = os.Remove(soundsDir)
	}
	return nil
}
