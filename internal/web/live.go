package web

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Kinds of changes the browser refreshes for, besides bot.Change kinds.
const changeRuns = "runs"

// liveGap collects the changes of a short burst into one message, a join
// sends several voice state updates.
const liveGap = 300 * time.Millisecond

// hub passes changes to the browsers that listen on /events. The app runs as
// one process, so the subscribers can live in memory.
type hub struct {
	mu     sync.Mutex
	subs   map[chan string]struct{}
	closed bool
}

func newHub() *hub { return &hub{subs: map[chan string]struct{}{}} }

func (h *hub) notify(kind string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- kind:
		default:
		}
	}
}

func (h *hub) subscribe() (chan string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, false
	}
	ch := make(chan string, 16)
	h.subs[ch] = struct{}{}
	return ch, true
}

func (h *hub) unsubscribe(ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
}

// close ends every stream, so a shutdown does not wait for them.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}

// StopLive ends the open /events streams; call it when the HTTP server shuts down.
func (s *Server) StopLive() { s.live.close() }

// events streams the kinds of changes as server-sent events. The page then
// fetches itself and swaps its live parts, without a reload.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	ch, ok := s.live.subscribe()
	if !ok {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	defer s.live.unsubscribe(ch)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	// nginx in front of Cloudron apps buffers responses otherwise
	h.Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()
	pending := map[string]bool{}
	var send <-chan time.Time
	for {
		select {
		case <-r.Context().Done():
			return
		case kind, open := <-ch:
			if !open {
				return
			}
			pending[kind] = true
			if send == nil {
				send = time.After(liveGap)
			}
		case <-send:
			send = nil
			for kind := range pending {
				fmt.Fprintf(w, "data: %s\n\n", kind)
			}
			clear(pending)
			flusher.Flush()
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}
