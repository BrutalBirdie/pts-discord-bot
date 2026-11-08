package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
)

func TestUploadZipAndReview(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	name := func(n string) string { return n + "-" + f.tag + ".txt" }

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	for _, n := range []string{"sounds/" + name("one"), name("two"), "__MACOSX/sounds/._" + name("one"), ".DS_Store", "inner.zip", "sounds/"} {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(n, "/") {
			fmt.Fprint(w, "text of "+n)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	upload := func(accept string, files map[string][]byte) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("csrf", "tok")
		mw.WriteField("tag", "Loud")
		for _, n := range slices.Sorted(maps.Keys(files)) {
			fw, _ := mw.CreateFormFile("file", n)
			fw.Write(files[n])
		}
		mw.Close()
		req := httptest.NewRequest("POST", "/media", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Origin", "https://bot.example")
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.cloud})
		rec := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(rec, req)
		return rec
	}

	rec := upload("", map[string][]byte{"pack.zip": zbuf.Bytes(), name("three"): []byte("three")})
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("upload with a problem: %d %s", rec.Code, body)
	}
	for _, want := range []string{"Uploaded 3 files", "3 other files", "Not added:", "inner.zip: archives inside pack.zip are not unpacked.", "Add tags to all 3", "Review one by one",
		`id="sheet-upload"`, "data-open-now", `id="tags-batch-sheet"`, `id="tags-batch-page"`} {
		if !strings.Contains(body, want) {
			t.Errorf("batch view: missing %q", want)
		}
	}
	if strings.Contains(body, "DS_Store") || strings.Contains(body, "._one") {
		t.Errorf("batch view lists files macOS adds")
	}
	var files []store.Media
	for _, n := range []string{"one", "two", "three"} {
		m, err := f.st.MediaByName(ctx, name(n))
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if !slices.Equal(m.Tags, []string{"loud"}) {
			t.Errorf("%s tags: %v", n, m.Tags)
		}
		files = append(files, m)
	}
	batch, err := f.st.FindMedia(ctx, store.MediaQuery{Scope: store.All, Batch: files[0].Batch})
	if err != nil || len(batch) != 3 || files[0].Batch == "" {
		t.Fatalf("batch %q: %d files, %v", files[0].Batch, len(batch), err)
	}

	tag := url.Values{"batch": {files[0].Batch}, "tag": {"Fresh", "loud"}, "next": {"/media?batch=" + files[0].Batch}}
	if rec := f.do(f.cloud, "POST", "/media/tags", tag); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/media?batch="+files[0].Batch+"&done=tagged" {
		t.Fatalf("tag all: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if body := f.do(f.cloud, "GET", "/media?batch="+files[0].Batch+"&done=tagged", nil).Body.String(); !strings.Contains(body, "data-open-now") || !strings.Contains(body, "Add more tags to all 3") {
		t.Errorf("after tag all the Uploaded sheet does not ask to review")
	}
	if body := f.do(f.cloud, "GET", "/media?batch="+files[0].Batch, nil).Body.String(); strings.Contains(body, "data-open-now") || strings.Contains(body, "data-upload-fallback") || !strings.Contains(body, "Showing the 3 files of this upload.") {
		t.Errorf("a later visit of the upload asks again instead of showing a line")
	}
	for _, m := range files {
		if got, _ := f.st.Media(ctx, m.ID); !slices.Equal(got.Tags, []string{"fresh", "loud"}) {
			t.Errorf("%s tags after tag all: %v", m.Name, got.Tags)
		}
	}
	if rec := f.do(f.cloud, "POST", "/media/tags", url.Values{"id": {fmt.Sprint(files[2].ID)}, "tags": {"a, b"}}); rec.Code != http.StatusSeeOther {
		t.Errorf("tag selected: %d", rec.Code)
	} else if got, _ := f.st.Media(ctx, files[2].ID); !slices.Equal(got.Tags, []string{"a", "b", "fresh", "loud"}) {
		t.Errorf("tags of a selected file: %v", got.Tags)
	}

	first, second := batch[0], batch[1]
	rec = f.do(f.cloud, "GET", fmt.Sprintf("/media?batch=%s&edit=%d", first.Batch, first.ID), nil)
	body = rec.Body.String()
	for _, want := range []string{"File 1 of 3 from this upload", "Save and next", "data-edit-sheet", fmt.Sprintf(`name="next" value="/media?batch=%s&amp;edit=%d"`, first.Batch, second.ID)} {
		if !strings.Contains(body, want) {
			t.Errorf("review sheet: missing %q", want)
		}
	}
	save := url.Values{"name": {first.Name}, "display_name": {"First"}, "tag": {"fresh"}, "next": {fmt.Sprintf("/media?batch=%s&edit=%d", first.Batch, second.ID)}}
	if rec := f.do(f.cloud, "POST", fmt.Sprintf("/media/%d", first.ID), save); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), fmt.Sprintf("edit=%d", second.ID)) {
		t.Errorf("save and next: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if body := f.do(f.cloud, "GET", fmt.Sprintf("/media?batch=%s&edit=%d", first.Batch, batch[2].ID), nil).Body.String(); !strings.Contains(body, "Save and finish") {
		t.Errorf("last file of the review has no Save and finish")
	}

	rec = upload("", map[string][]byte{name("single"): []byte("x")})
	single, _ := f.st.MediaByName(ctx, name("single"))
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || loc != fmt.Sprintf("/media?batch=%s&edit=%d&done=uploaded", single.Batch, single.ID) {
		t.Errorf("single upload: %d %s", rec.Code, loc)
	}

	rec = upload("application/json", map[string][]byte{name("json"): []byte("x"), name("single"): []byte("x")})
	var out struct {
		Added []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"added"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("json answer: %v %s", err, rec.Body.String())
	}
	if len(out.Added) != 1 || out.Added[0].Name != name("json") || out.Added[0].Kind != store.KindFile || len(out.Problems) != 1 {
		t.Errorf("json answer: %+v", out)
	}
}
