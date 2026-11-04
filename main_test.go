package main

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPlayer(t *testing.T) *player {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gong.ogg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &player{cfg: config{soundsDir: dir, maxUploadSize: 1024}}
}

func statusOf(err error) int {
	var he *httpError
	if errors.As(err, &he) {
		return he.status
	}
	return 0
}

func TestParsePlayRequestJSON(t *testing.T) {
	p := testPlayer(t)
	cases := map[string]int{
		`{"channel_id":"475645920659243008","file":"gong.ogg"}`: 0,
		`{"file":"missing.ogg"}`:                                http.StatusNotFound,
		`{"file":"../etc/passwd"}`:                              http.StatusBadRequest,
		`{"channel_id":475645920659243008,"file":"gong.ogg"}`:   http.StatusBadRequest,
	}
	for body, want := range cases {
		r := httptest.NewRequest("POST", "/play", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		req, cleanup, err := p.parsePlayRequest(httptest.NewRecorder(), r)
		cleanup()
		if got := statusOf(err); got != want {
			t.Errorf("%s: got status %d (%v), want %d", body, got, err, want)
		}
		if want == 0 && req.ChannelID.String() != "475645920659243008" {
			t.Errorf("%s: channel_id = %s", body, req.ChannelID)
		}
	}
}

func TestParsePlayRequestRaw(t *testing.T) {
	p := testPlayer(t)
	r := httptest.NewRequest("POST", "/play?channel_id=475645920659243008", strings.NewReader("audio-bytes"))
	r.Header.Set("Content-Type", "audio/ogg")
	req, cleanup, err := p.parsePlayRequest(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(req.path); string(b) != "audio-bytes" {
		t.Errorf("temp file content = %q", b)
	}
	cleanup()
	if _, err := os.Stat(req.path); !os.IsNotExist(err) {
		t.Errorf("temp file not removed")
	}

	r = httptest.NewRequest("POST", "/play", bytes.NewReader(make([]byte, 2048)))
	r.Header.Set("Content-Type", "application/octet-stream")
	_, cleanup, err = p.parsePlayRequest(httptest.NewRecorder(), r)
	cleanup()
	if statusOf(err) != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized upload: got %v", err)
	}

	r = httptest.NewRequest("POST", "/play", strings.NewReader(""))
	_, cleanup, err = p.parsePlayRequest(httptest.NewRecorder(), r)
	cleanup()
	if statusOf(err) != http.StatusBadRequest {
		t.Errorf("empty body: got %v", err)
	}
}

func TestParsePlayRequestMultipart(t *testing.T) {
	p := testPlayer(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("channel_id", "475645920659243008")
	fw, _ := mw.CreateFormFile("file", "adhan.mp3")
	_, _ = fw.Write([]byte("mp3-bytes"))
	_ = mw.Close()

	r := httptest.NewRequest("POST", "/play", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	req, cleanup, err := p.parsePlayRequest(httptest.NewRecorder(), r)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if req.File != "adhan.mp3" || req.ChannelID.String() != "475645920659243008" {
		t.Errorf("got file=%q channel=%s", req.File, req.ChannelID)
	}
	if b, _ := os.ReadFile(req.path); string(b) != "mp3-bytes" {
		t.Errorf("temp file content = %q", b)
	}
}
