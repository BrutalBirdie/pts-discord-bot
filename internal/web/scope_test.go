package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/BrutalBirdie/pts-discord-bot/internal/access"
	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/media"
	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
	"github.com/BrutalBirdie/pts-discord-bot/internal/runner"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// scopeFixture is a Discord user approved for server A who is also a member
// of server B, with tasks, messages and media in A, B and shared ones.
type scopeFixture struct {
	t                *testing.T
	s                *Server
	st               *store.Store
	a, b, tag        string
	discord, cloud   string // session cookies
	taskA, taskB     int64
	msgA, msgB, msgS int64
	medA, medB, medS int64
	trashA, trashB   int64 // trashed messages
	trashS           int64 // trashed shared media
}

func snowflake() string { return fmt.Sprint(100000000000000000 + rand.Int64N(800000000000000000)) }

// newScopeFixture needs a database, test/e2e/run.sh provides one. Names and
// IDs are random, so the test can run again on the same database.
func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.CreateSchema(ctx); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	dir := media.Dir(t.TempDir())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := bot.New(bot.Config{Logger: log})
	s, err := New(Config{Origin: "https://bot.example", Admins: access.Parse("ann", "bot-admins"), MediaDir: dir}, st, b, runner.New(st, b, dir, log), log)
	must(err)
	f := &scopeFixture{t: t, s: s, st: st, a: snowflake(), b: snowflake(), tag: fmt.Sprintf("z%d", rand.Int64N(1e12))}

	uid := snowflake()
	_, err = st.UpsertDiscordUser(ctx, store.DiscordUser{ID: uid, Username: "u" + uid})
	must(err)
	_, err = st.CreateAccessRequest(ctx, uid, []string{f.a}, "")
	must(err)
	must(st.ApproveAccess(ctx, uid, []string{f.a}, "ann"))
	f.discord, f.cloud = randomToken(), randomToken()
	must(st.CreateSession(ctx, f.discord, store.Session{Username: store.DiscordSubject(uid), DisplayName: "Mira", Provider: store.ProviderDiscord, GuildIDs: []string{f.a, f.b}, CSRFToken: "tok"}))
	must(st.CreateSession(ctx, f.cloud, store.Session{Username: "ann", DisplayName: "Ann", CSRFToken: "tok"}))

	addMedia := func(owner string) int64 {
		id, err := st.AddMedia(ctx, store.Media{Name: f.name("media", owner) + ".png", Kind: store.KindImage, ContentType: "image/png", SizeBytes: 3, UploadedBy: "t", GuildID: owner})
		must(err)
		must(os.WriteFile(dir.Path(id), []byte("png"), 0o600))
		return id
	}
	f.medA, f.medB, f.medS = addMedia(f.a), addMedia(f.b), addMedia("")
	sound, err := st.AddMedia(ctx, store.Media{Name: f.name("sound", "") + ".ogg", Kind: store.KindSound, SizeBytes: 3, UploadedBy: "t"})
	must(err)
	f.trashS = sound
	addMessage := func(prefix, owner string) int64 {
		id, err := st.CreateMessage(ctx, messages.Message{Name: f.name(prefix, owner), Content: "hi", Embeds: []messages.Embed{}, GuildID: owner}, "t")
		must(err)
		return id
	}
	f.msgA, f.msgB, f.msgS = addMessage("msg", f.a), addMessage("msg", f.b), addMessage("msg", "")
	f.trashA, f.trashB = addMessage("trash", f.a), addMessage("trash", f.b)
	addTask := func(owner string) int64 {
		id, err := st.CreateTask(ctx, tasks.Task{Name: f.name("task", owner), GuildID: owner, Busy: tasks.BusyWait, Trigger: tasks.Trigger{Kind: tasks.Manual},
			Steps: []tasks.Step{{Type: tasks.Message, ChannelID: "1", Text: "hi"}}}, "t")
		must(err)
		return id
	}
	f.taskA, f.taskB = addTask(f.a), addTask(f.b)
	must(st.Trash(ctx, store.TrashMessage, f.trashA, "t"))
	must(st.Trash(ctx, store.TrashMessage, f.trashB, "t"))
	must(st.Trash(ctx, store.TrashMedia, f.trashS, "t"))
	return f
}

// name is unique per test run and tells the owner, for checks on list pages.
func (f *scopeFixture) name(prefix, owner string) string {
	o := "shared"
	switch owner {
	case f.a:
		o = "inA"
	case f.b:
		o = "inB"
	}
	return prefix + "-" + o + "-" + f.tag
}

func (f *scopeFixture) do(cookie, method, target string, form url.Values) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		form.Set("csrf", "tok")
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	if method == http.MethodPost {
		req.Header.Set("Origin", "https://bot.example")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestDiscordUserIsLimitedToApprovedServers(t *testing.T) {
	f := newScopeFixture(t)
	id := func(n int64) string { return fmt.Sprint(n) }
	post := func(kv ...string) url.Values {
		v := url.Values{}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}
	type req struct {
		method, target string
		form           url.Values
	}
	var hidden []req
	for _, tid := range []string{id(f.taskB)} {
		hidden = append(hidden, req{"GET", "/tasks/" + tid, nil}, req{"POST", "/tasks/" + tid, post("op", "save", "guild", f.b, "name", "x")},
			req{"POST", "/tasks/" + tid, post("op", "save", "guild", f.a, "name", "x")})
		for _, action := range []string{"run", "pause", "resume", "duplicate", "delete"} {
			hidden = append(hidden, req{"POST", "/tasks/" + tid + "/" + action, post()})
		}
	}
	for _, mid := range []string{id(f.msgB), id(f.msgS)} {
		hidden = append(hidden, req{"GET", "/messages/" + mid, nil}, req{"POST", "/messages/" + mid, post("op", "save", "guild", f.a, "name", "x", "content", "x")})
		for _, action := range []string{"send", "duplicate", "delete"} {
			hidden = append(hidden, req{"POST", "/messages/" + mid + "/" + action, post("channel", "1")})
		}
	}
	for _, mid := range []string{id(f.medB), id(f.medS)} {
		hidden = append(hidden, req{"GET", "/media/" + mid + "/file", nil}, req{"POST", "/media/" + mid, post("name", "x.png")},
			req{"POST", "/media/" + mid + "/play", post("channel", "1")}, req{"POST", "/media/" + mid + "/delete", post()})
	}
	hidden = append(hidden,
		req{"GET", "/media/" + id(f.trashS) + "/file", nil},
		req{"POST", "/trash/message/" + id(f.trashB) + "/restore", post()},
		req{"POST", "/trash/media/" + id(f.trashS) + "/restore", post()},
		req{"GET", "/servers/" + f.b, nil},
		req{"POST", "/servers/" + f.b + "/stop", post()}, req{"POST", "/servers/" + f.b + "/leave", post()},
		req{"POST", "/servers/" + f.b + "/join", post("channel", "1")}, req{"POST", "/servers/" + f.b + "/play", post("channel", "1", "media", id(f.medA))},
		// creating or moving items to server B, or making them shared
		req{"POST", "/tasks/new", post("op", "save", "guild", f.b, "name", "new task", "kind", "manual")},
		req{"POST", "/tasks/new", post("op", "reload", "guild", f.b)},
		req{"POST", "/tasks/" + id(f.taskA), post("op", "save", "guild", f.b, "name", "moved", "kind", "manual")},
		req{"POST", "/messages/new", post("op", "save", "guild", f.b, "name", "new msg", "content", "x")},
		req{"POST", "/messages/new", post("op", "save", "guild", "", "name", "new msg", "content", "x")},
		req{"POST", "/messages/" + id(f.msgA), post("op", "save", "guild", f.b, "name", "moved", "content", "x")},
		req{"POST", "/media/url", post("guild", f.b, "url", "https://example.com/x.ogg")},
		req{"POST", "/media/url", post("guild", "", "url", "https://example.com/x.ogg")},
	)
	for _, c := range hidden {
		if rec := f.do(f.discord, c.method, c.target, c.form); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s %v: %d, want 404", c.method, c.target, c.form, rec.Code)
		}
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("csrf", "tok")
	mw.WriteField("guild", f.b)
	fw, _ := mw.CreateFormFile("file", "x.png")
	fw.Write([]byte("png"))
	mw.Close()
	up := httptest.NewRequest("POST", "/media", &buf)
	up.Header.Set("Content-Type", mw.FormDataContentType())
	up.Header.Set("Origin", "https://bot.example")
	up.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.discord})
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, up)
	if rec.Code != http.StatusNotFound {
		t.Errorf("upload to server B: %d", rec.Code)
	}

	for _, c := range []req{{"GET", "/admin", nil}, {"GET", "/admin/access", nil}, {"POST", "/trash/message/" + id(f.trashA) + "/purge", post()},
		{"POST", "/trash/empty", post()}, {"POST", "/servers/refresh", post()}, {"POST", "/admin/settings", post()}} {
		if rec := f.do(f.discord, c.method, c.target, c.form); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403", c.method, c.target, rec.Code)
		}
	}

	ctx := context.Background()
	inB := store.GuildScope(false, f.b)
	if list, _ := f.st.ListTasks(ctx, inB); len(list) != 1 {
		t.Errorf("tasks in B: %d", len(list))
	}
	if list, _ := f.st.ListMessages(ctx, inB); len(list) != 1 {
		t.Errorf("messages in B: %d", len(list))
	}
	if list, _ := f.st.ListMedia(ctx, inB, ""); len(list) != 1 {
		t.Errorf("media in B: %d", len(list))
	}
	if task, _ := f.st.Task(ctx, f.taskA); task.GuildID != f.a {
		t.Errorf("task moved to %s", task.GuildID)
	}

	q := "?q=" + f.tag
	visible := map[string]string{"/tasks" + q: "task-inA", "/messages" + q: "msg-inA", "/media" + q: "media-inA", "/trash" + q: "trash-inA", "/": "", "/servers": "",
		"/tasks/" + id(f.taskA): "task-inA", "/messages/" + id(f.msgA): "msg-inA", "/messages/new": "", "/tasks/new": "", "/sounds" + q: "", "/members?q=a": ""}
	for target, want := range visible {
		rec := f.do(f.discord, "GET", target, nil)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("GET %s: %d, missing %q", target, rec.Code, want)
		}
		for _, other := range []string{"inB-" + f.tag, "shared-" + f.tag} {
			if strings.Contains(body, other) {
				t.Errorf("GET %s shows %s", target, other)
			}
		}
		if strings.Contains(body, `href="/admin"`) {
			t.Errorf("GET %s: admin link", target)
		}
	}
	if rec := f.do(f.discord, "GET", "/media/"+id(f.medA)+"/file", nil); rec.Code != http.StatusOK {
		t.Errorf("file of A: %d", rec.Code)
	}
	if rec := f.do(f.discord, "GET", "/sounds?guild="+f.b+"&q="+f.tag, nil); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("sounds of B: %s", rec.Body.String())
	}

	// Cloudron users see everything as before
	for target, want := range map[string]string{"/tasks?q=" + f.tag: "task-inB", "/messages?q=" + f.tag: "msg-shared", "/media?q=" + f.tag: "media-inB", "/messages/" + id(f.msgB): "msg-inB"} {
		if rec := f.do(f.cloud, "GET", target, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("cloudron GET %s: %d, missing %q", target, rec.Code, want)
		}
	}
}

func TestDiscordUserWithoutGrantSeesOnlyTheStartPage(t *testing.T) {
	f := newScopeFixture(t)
	token := randomToken()
	uid := snowflake()
	ctx := context.Background()
	if _, err := f.st.UpsertDiscordUser(ctx, store.DiscordUser{ID: uid, Username: "p" + uid}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreateSession(ctx, token, store.Session{Username: store.DiscordSubject(uid), Provider: store.ProviderDiscord, GuildIDs: []string{f.a}, CSRFToken: "tok"}); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(token, "GET", "/", nil); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `href="/tasks"`) {
		t.Errorf("start page: %d", rec.Code)
	}
	for _, target := range []string{"/tasks", "/tasks/" + fmt.Sprint(f.taskA), "/messages", "/media", "/trash", "/servers"} {
		if rec := f.do(token, "GET", target, nil); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s: %d", target, rec.Code)
		}
	}
}
