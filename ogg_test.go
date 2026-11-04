package main

import (
	"errors"
	"io"
	"os/exec"
	"testing"
)

func TestOggOpusReaderFrameCount(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:a", "libopus", "-ar", "48000", "-ac", "2", "-frame_duration", "20",
		"-f", "ogg", "pipe:1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()

	r := newOggOpusReader(out)
	frames := 0
	for {
		pkt, err := r.NextPacket()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(pkt) == 0 {
			t.Fatal("empty packet")
		}
		frames++
	}
	// 2s at 20ms per frame, plus the encoder's pre-skip padding frame.
	if frames < 100 || frames > 102 {
		t.Fatalf("got %d frames, want about 100", frames)
	}
}
