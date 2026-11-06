package bot

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRetryAfter(t *testing.T) {
	h := http.Header{"Retry-After": {"7"}}
	for _, c := range []struct {
		body string
		h    http.Header
		want time.Duration
	}{
		{`{"message":"You are being rate limited.","retry_after":64.57,"global":false}`, h, 64570 * time.Millisecond},
		{`not json`, h, 7 * time.Second},
		{`{}`, http.Header{}, time.Minute},
	} {
		if got := RetryAfter(c.h, []byte(c.body)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.body, got, c.want)
		}
	}
}

type fakeRT struct{ rs *http.Response }

func (f fakeRT) RoundTrip(*http.Request) (*http.Response, error) { return f.rs, nil }

func TestLimitTransport(t *testing.T) {
	var limits profileLimits
	start := time.Now()
	body := `{"retry_after":120,"global":false}`
	rt := &limitTransport{base: fakeRT{&http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}}, limits: &limits}
	req, _ := http.NewRequest(http.MethodPatch, "https://discord.com/api/v10/guilds/123456789012345678/members/@me", nil)
	rs, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(rs.Body); string(b) != body {
		t.Errorf("body not kept for disgo: %q", b)
	}
	until, ok := limits.since(123456789012345678, start)
	if !ok || until.Sub(start) < 119*time.Second {
		t.Errorf("limit %v %v", until, ok)
	}
	if _, ok := limits.since(123456789012345678, time.Now().Add(time.Second)); ok {
		t.Error("a limit from before the call is reported")
	}
	if _, ok := profileGuild("/api/v10/guilds/1/members/@me/nick"); ok {
		t.Error("other route matched")
	}
	other, _ := http.NewRequest(http.MethodPatch, "https://discord.com/api/v10/guilds/223456789012345678/members/42", nil)
	rt.base = fakeRT{&http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}}
	_, _ = rt.RoundTrip(other)
	if _, ok := limits.since(223456789012345678, start); ok {
		t.Error("a member edit counted as profile change")
	}
}
