// Package media stores uploaded files on disk and finds out what they contain.
package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// Dir holds one file per media row, named by its id.
type Dir string

func (d Dir) Path(id int64) string { return filepath.Join(string(d), strconv.FormatInt(id, 10)) }

func (d Dir) Temp() (*os.File, error) { return os.CreateTemp(string(d), ".upload-*") }

// CleanName keeps a file name readable and safe as an attachment name for Discord.
func CleanName(name string) (string, error) {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '"' || unicode.IsControl(r):
			return -1
		case unicode.IsSpace(r):
			return '_'
		}
		return r
	}, name)
	name = strings.TrimLeft(name, ".")
	if name == "" || len(name) > 200 {
		return "", errors.New("the file name must have 1 to 200 characters")
	}
	return name, nil
}

var imageExt = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true}

// EmbeddableImage reports whether Discord can show the file inside an embed.
func EmbeddableImage(name string) bool { return imageExt[strings.ToLower(filepath.Ext(name))] }

type probeOut struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecType   string `json:"codec_type"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		Disposition struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
	} `json:"streams"`
}

// Probe fills kind, duration, size and content type of an uploaded file.
func Probe(ctx context.Context, path, name string) (store.Media, error) {
	m := store.Media{Name: name, Kind: store.KindFile}
	fi, err := os.Stat(path)
	if err != nil {
		return m, err
	}
	m.SizeBytes = fi.Size()
	m.ContentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if m.ContentType == "" {
		m.ContentType = "application/octet-stream"
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type,width,height:stream_disposition=attached_pic", "-of", "json", path).Output()
	if err != nil {
		// not a media file ffmpeg understands, it can still be sent as attachment
		if EmbeddableImage(name) {
			m.Kind = store.KindImage
		}
		return m, nil
	}
	var p probeOut
	if err := json.Unmarshal(out, &p); err != nil {
		return m, fmt.Errorf("reading ffprobe output: %w", err)
	}
	var audio, video bool
	for _, s := range p.Streams {
		switch {
		case s.CodecType == "audio":
			audio = true
		case s.CodecType == "video" && s.Disposition.AttachedPic == 0:
			video = true
			m.Width, m.Height = s.Width, s.Height
		}
	}
	if d, err := strconv.ParseFloat(p.Format.Duration, 64); err == nil {
		m.DurationMS = int(d * 1000)
	}
	switch {
	case imageExt[strings.ToLower(filepath.Ext(name))] && video:
		m.Kind, m.DurationMS = store.KindImage, 0
	case video:
		m.Kind = store.KindVideo
	case audio:
		m.Kind = store.KindSound
	}
	return m, nil
}

// HasAudio reports whether the file can be played in a voice channel.
func HasAudio(m store.Media) bool { return m.Kind == store.KindSound || m.Kind == store.KindVideo }

var avatarTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// AvatarProblem says why Discord would not take the image as avatar, empty when it would.
// https://docs.discord.com/developers/reference#image-data
func AvatarProblem(m store.Media) string {
	if m.Kind != store.KindImage || !avatarTypes[m.ContentType] {
		return m.Name + " is not a PNG, JPEG, GIF or WebP image"
	}
	if m.SizeBytes > tasks.MaxAvatarBytes {
		return fmt.Sprintf("%s has %.1f MB, an avatar may have at most %d MB", m.Name, float64(m.SizeBytes)/(1<<20), tasks.MaxAvatarBytes>>20)
	}
	return ""
}

// AvatarType is the type of avatar image data, empty when Discord does not take it.
func AvatarType(data []byte) string {
	if t := http.DetectContentType(data); avatarTypes[t] {
		return t
	}
	return ""
}
