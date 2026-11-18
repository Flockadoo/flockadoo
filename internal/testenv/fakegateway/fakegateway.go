// Package fakegateway is the node's connect-gateway for tests (plan section
// 17.2). It accepts the client's WebSocket with a bearer check, runs yamux in
// client mode on top, and opens one stream per request the way the node's
// reverse proxy does (node: pkg/api/connect/service.go, Accept and newProxy).
// A second session with the same key replaces the first, as on the node.
//
// The controls break the connection in the ways the real one breaks: Drop
// (the network goes away), Replace (another client took the key), Blackhole
// (half-open: nothing is read or answered) and RejectNext (the next dial is
// answered with an error status).
package fakegateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// headerClientVersion is the header the client sends its version in.
const headerClientVersion = "X-Inferoute-Client-Version"

// Gateway is a running fake gateway. It stops when the test ends.
type Gateway struct {
	// URL is the platform address to give the client: it dials
	// ws://<host>/api/connect/ws.
	URL string

	apiKey string
	srv    *httptest.Server

	mu         sync.Mutex
	hijacked   map[string]net.Conn // by remote address, until its session starts
	dials      []Dial
	rejectNext *rejection
	live       *session
	changed    chan struct{}
}

// Dial is one connection attempt by a client.
type Dial struct {
	At            time.Time
	Status        int // 101 when the session was accepted
	Authorization string
	ClientVersion string
}

type rejection struct {
	status int
	body   string
}

type session struct {
	ws     *websocket.Conn
	ys     *yamux.Session
	raw    net.Conn
	hole   *holeConn
	client *http.Client
}

// New starts a gateway that accepts sessions bearing apiKey.
func New(t testing.TB, apiKey string) *Gateway {
	t.Helper()
	g := &Gateway{apiKey: apiKey, hijacked: map[string]net.Conn{}, changed: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/connect/ws", g.accept)
	g.srv = httptest.NewUnstartedServer(mux)
	g.srv.Config.ConnState = func(c net.Conn, st http.ConnState) {
		if st == http.StateHijacked {
			g.mu.Lock()
			g.hijacked[c.RemoteAddr().String()] = c
			g.mu.Unlock()
		}
	}
	g.srv.Start()
	g.URL = g.srv.URL
	t.Cleanup(func() {
		g.mu.Lock()
		s := g.live
		g.live = nil
		g.mu.Unlock()
		if s != nil {
			_ = s.ys.Close()
		}
		g.srv.Close()
	})
	return g
}

func (g *Gateway) accept(w http.ResponseWriter, r *http.Request) {
	d := Dial{At: time.Now(), Authorization: r.Header.Get("Authorization"), ClientVersion: r.Header.Get(headerClientVersion)}

	g.mu.Lock()
	rej := g.rejectNext
	g.rejectNext = nil
	g.mu.Unlock()
	switch {
	case rej != nil:
		g.record(d, rej.status)
		writeError(w, rej.status, rej.body)
		return
	case d.Authorization != "Bearer "+g.apiKey:
		g.record(d, http.StatusUnauthorized)
		writeError(w, http.StatusUnauthorized, "invalid API key")
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		g.record(d, http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	raw := g.hijacked[r.RemoteAddr]
	delete(g.hijacked, r.RemoteAddr)
	g.mu.Unlock()

	// As on the node: the session outlives the upgrade request.
	netConn := websocket.NetConn(context.WithoutCancel(r.Context()), ws, websocket.MessageBinary)
	hole := &holeConn{Conn: netConn, closed: make(chan struct{})}
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.LogOutput = io.Discard
	ys, err := yamux.Client(hole, cfg)
	if err != nil {
		g.record(d, http.StatusInternalServerError)
		_ = ws.Close(websocket.StatusInternalError, "yamux")
		return
	}
	s := &session{
		ws: ws, ys: ys, raw: raw, hole: hole,
		// The node's proxy transport: one stream per request, no reuse.
		client: &http.Client{Transport: &http.Transport{
			DialContext:       func(context.Context, string, string) (net.Conn, error) { return ys.Open() },
			DisableKeepAlives: true,
		}},
	}

	g.mu.Lock()
	g.dials = append(g.dials, withStatus(d, http.StatusSwitchingProtocols))
	old := g.live
	g.live = s
	g.notify()
	g.mu.Unlock()
	if old != nil {
		_ = old.ys.Close()
	}

	<-ys.CloseChan()
	g.mu.Lock()
	if g.live == s {
		g.live = nil
		g.notify()
	}
	g.mu.Unlock()
}

func withStatus(d Dial, status int) Dial { d.Status = status; return d }

func (g *Gateway) record(d Dial, status int) {
	g.mu.Lock()
	g.dials = append(g.dials, withStatus(d, status))
	g.notify()
	g.mu.Unlock()
}

// notify wakes every waiter. Call it with g.mu held.
func (g *Gateway) notify() {
	close(g.changed)
	g.changed = make(chan struct{})
}

// writeError answers the way the node does: {"error": message}.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Connected reports whether a session is live.
func (g *Gateway) Connected() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.live != nil
}

// Dials returns every connection attempt so far, in order.
func (g *Gateway) Dials() []Dial {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Dial(nil), g.dials...)
}

// AwaitSession waits until a session is live.
func (g *Gateway) AwaitSession(t testing.TB) {
	t.Helper()
	g.await(t, "a live session", func() bool { return g.live != nil })
}

// AwaitDials waits until n dials have arrived.
func (g *Gateway) AwaitDials(t testing.TB, n int) {
	t.Helper()
	g.await(t, fmt.Sprintf("%d dials", n), func() bool { return len(g.dials) >= n })
}

func (g *Gateway) await(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		g.mu.Lock()
		ok, changed := cond(), g.changed
		g.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatalf("fakegateway: timed out waiting for %s", what)
		}
	}
}

// Do sends req to the client over the live session, as the node's proxy
// does: a new stream, http://provider as the host. The caller reads and
// closes the response body.
func (g *Gateway) Do(req *http.Request) (*http.Response, error) {
	g.mu.Lock()
	s := g.live
	g.mu.Unlock()
	if s == nil {
		return nil, errors.New("fakegateway: no live session")
	}
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host, out.Host, out.RequestURI = "http", "provider", "provider", ""
	return s.client.Do(out)
}

// Drop closes the TCP connection under the live session with no WebSocket
// close frame, as when the network goes away.
func (g *Gateway) Drop() {
	if s := g.takeLive(); s != nil && s.raw != nil {
		_ = s.raw.Close()
	}
}

// Replace closes the live session the way the node does when another client
// connects with the same key.
func (g *Gateway) Replace() {
	if s := g.takeLive(); s != nil {
		_ = s.ys.Close()
	}
}

// ReplaceWithReason closes the live session the way the gateway will once it
// names a replacement (decision DC8): a WebSocket close with code and
// reason, here the client's CloseReplaced.
func (g *Gateway) ReplaceWithReason(code int, reason string) {
	if s := g.takeLive(); s != nil {
		_ = s.ws.Close(websocket.StatusCode(code), reason)
		_ = s.ys.Close()
	}
}

// OpenStream opens a raw stream to the client on the live session, for a
// test that writes something other than a well-formed request.
func (g *Gateway) OpenStream() (net.Conn, error) {
	g.mu.Lock()
	s := g.live
	g.mu.Unlock()
	if s == nil {
		return nil, errors.New("fakegateway: no live session")
	}
	return s.ys.Open()
}

// Blackhole makes the live session half-open: from now on the gateway reads
// nothing and answers nothing, and keeps the connection open. Only the
// client's keepalive can notice.
func (g *Gateway) Blackhole() {
	g.mu.Lock()
	s := g.live
	g.mu.Unlock()
	if s != nil {
		s.hole.stop()
	}
}

// RejectNext answers the next dial with status and the node's error body
// for msg (401, 426, 503), without upgrading.
func (g *Gateway) RejectNext(status int, msg string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rejectNext = &rejection{status: status, body: msg}
}

func (g *Gateway) takeLive() *session {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.live
	if s != nil {
		g.live = nil
		g.notify()
	}
	return s
}

// holeConn is the connection under the gateway's yamux session. Once
// stopped, every read blocks until the connection is closed, and whatever
// arrived is dropped unread.
type holeConn struct {
	net.Conn
	mu        sync.Mutex
	black     bool
	closeOnce sync.Once
	closed    chan struct{}
}

func (h *holeConn) stop() {
	h.mu.Lock()
	h.black = true
	h.mu.Unlock()
}

func (h *holeConn) isBlack() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.black
}

func (h *holeConn) Read(p []byte) (int, error) {
	if !h.isBlack() {
		n, err := h.Conn.Read(p)
		if !h.isBlack() {
			return n, err
		}
	}
	<-h.closed
	return 0, net.ErrClosed
}

func (h *holeConn) Close() error {
	h.closeOnce.Do(func() { close(h.closed) })
	return h.Conn.Close()
}
