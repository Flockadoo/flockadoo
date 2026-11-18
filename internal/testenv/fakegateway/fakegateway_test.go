package fakegateway_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakegateway"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/connect"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/logger"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/version"
	"go.uber.org/zap"
)

// Plan item A2, section 17.2: the fake gateway's behaviours. Most run the
// client's real connect.Client against it; the ones that need short yamux
// timeouts, or the raw connection, use a bare session.

func TestMain(m *testing.M) {
	logger.SetDefaultLogger(&logger.Logger{Logger: zap.NewNop()})
	os.Exit(m.Run())
}

func echoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Seen-Path", r.URL.Path)
		w.Header().Set("X-Seen-Session", r.Header.Get("X-Session-Key"))
		_, _ = w.Write(append([]byte("echo:"), body...))
	})
}

// runClient runs the real tunnel client until the test ends.
func runClient(t *testing.T, gw *fakegateway.Gateway, key string, h http.Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = connect.New(gw.URL, key, h, nil).Run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
}

func TestDoReachesTheClientsHandlerThroughTheTunnel(t *testing.T) {
	gw := fakegateway.New(t, "key")
	runClient(t, gw, "key", echoHandler())
	gw.AwaitSession(t)

	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("hi"))
	req.Header.Set("X-Session-Key", "s1")
	resp, err := gw.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "echo:hi" {
		t.Fatalf("got %d %q, want 200 echo:hi", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Seen-Path") != "/v1/chat/completions" || resp.Header.Get("X-Seen-Session") != "s1" {
		t.Fatalf("the handler saw path %q and session %q", resp.Header.Get("X-Seen-Path"), resp.Header.Get("X-Seen-Session"))
	}
}

func TestDialsAreRecordedWithTheirHeaders(t *testing.T) {
	gw := fakegateway.New(t, "key")
	runClient(t, gw, "key", echoHandler())
	gw.AwaitSession(t)
	dials := gw.Dials()
	if len(dials) != 1 {
		t.Fatalf("dials = %+v, want one", dials)
	}
	d := dials[0]
	if d.Status != http.StatusSwitchingProtocols || d.Authorization != "Bearer key" || d.ClientVersion != version.Version {
		t.Fatalf("dial = %+v, want 101 with the bearer key and client version %q", d, version.Version)
	}
}

func TestAWrongKeyIsRefusedWith401(t *testing.T) {
	gw := fakegateway.New(t, "key")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := connect.New(gw.URL, "wrong", echoHandler(), nil).Run(ctx)
	if !errors.Is(err, connect.ErrInvalidAPIKey) {
		t.Fatalf("Run = %v, want ErrInvalidAPIKey", err)
	}
	if dials := gw.Dials(); len(dials) != 1 || dials[0].Status != http.StatusUnauthorized {
		t.Fatalf("dials = %+v, want one refused with 401", dials)
	}
}

func TestRejectNextAnswersOnlyTheNextDial(t *testing.T) {
	gw := fakegateway.New(t, "key")
	gw.RejectNext(http.StatusServiceUnavailable, "connect gateway is not accepting sessions")
	runClient(t, gw, "key", echoHandler())
	gw.AwaitSession(t)
	dials := gw.Dials()
	if len(dials) != 2 || dials[0].Status != http.StatusServiceUnavailable || dials[1].Status != http.StatusSwitchingProtocols {
		t.Fatalf("dials = %+v, want 503 then 101", dials)
	}
}

func TestDropCutsTheConnectionAndTheClientDialsAgain(t *testing.T) {
	gw := fakegateway.New(t, "key")
	runClient(t, gw, "key", echoHandler())
	gw.AwaitSession(t)
	gw.Drop()
	gw.AwaitDials(t, 2)
	gw.AwaitSession(t)
	if dials := gw.Dials(); len(dials) != 2 || dials[1].Status != http.StatusSwitchingProtocols || !gw.Connected() {
		t.Fatalf("after a drop: dials %+v, connected %v; want a second accepted dial", dials, gw.Connected())
	}
}

// A drop is the network going away: the client's next read fails without a
// WebSocket close frame.
func TestDropSendsNoCloseFrame(t *testing.T) {
	gw := fakegateway.New(t, "key")
	ws := rawWebSocket(t, gw, "key")
	gw.AwaitSession(t)
	gw.Drop()
	err := waitClosed(t, ws)
	if code := websocket.CloseStatus(err); code != -1 {
		t.Fatalf("after a drop the client read close status %d (%v), want no close frame", code, err)
	}
}

// Replace is what the real gateway does to a session when another client
// connects with the same key: it closes the old one.
func TestReplaceClosesTheSession(t *testing.T) {
	gw := fakegateway.New(t, "key")
	sess := rawSession(t, gw, "key", time.Second, time.Second)
	gw.AwaitSession(t)
	gw.Replace()
	select {
	case <-sess.CloseChan():
	case <-time.After(3 * time.Second):
		t.Fatal("the replaced session is still open")
	}
	if gw.Connected() {
		t.Fatal("the gateway still reports a session after replacing it")
	}
}

func TestASecondDialReplacesTheFirst(t *testing.T) {
	gw := fakegateway.New(t, "key")
	first := rawSession(t, gw, "key", time.Second, time.Second)
	gw.AwaitSession(t)
	second := rawSession(t, gw, "key", time.Second, time.Second)
	select {
	case <-first.CloseChan():
	case <-time.After(3 * time.Second):
		t.Fatal("the first session survived a second dial with the same key")
	}
	if second.IsClosed() {
		t.Fatal("the second session was closed")
	}
	gw.AwaitSession(t)
	req, _ := http.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader("x"))
	resp, err := gw.Do(req)
	if err != nil {
		t.Fatalf("Do after the replacement: %v", err)
	}
	_ = resp.Body.Close()
}

// Blackhole is a half-open connection: the gateway stops reading and
// answering but does not close, so only the client's keepalive notices.
func TestBlackholeStopsAnsweringWithoutClosing(t *testing.T) {
	gw := fakegateway.New(t, "key")
	sess := rawSession(t, gw, "key", time.Hour, 200*time.Millisecond)
	gw.AwaitSession(t)
	if _, err := sess.Ping(); err != nil {
		t.Fatalf("ping before the blackhole: %v", err)
	}
	gw.Blackhole()
	if _, err := sess.Ping(); !errors.Is(err, yamux.ErrTimeout) {
		t.Fatalf("ping into the blackhole = %v, want a timeout", err)
	}
	if len(gw.Dials()) != 1 {
		t.Fatalf("dials = %+v", gw.Dials())
	}
}

func rawWebSocket(t *testing.T, gw *fakegateway.Gateway, key string) *websocket.Conn {
	t.Helper()
	wsURL, err := connect.DialURL(gw.URL)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+key)
	conn, resp, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{HTTPHeader: header})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

// rawSession dials gw like the client does, but with the given yamux
// keepalive interval and write timeout, and serves echoHandler on it.
func rawSession(t *testing.T, gw *fakegateway.Gateway, key string, keepAlive, writeTimeout time.Duration) *yamux.Session {
	t.Helper()
	conn := rawWebSocket(t, gw, key)
	ctx, cancel := context.WithCancel(context.Background())
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = keepAlive
	cfg.ConnectionWriteTimeout = writeTimeout
	cfg.LogOutput = io.Discard
	sess, err := yamux.Server(websocket.NetConn(ctx, conn, websocket.MessageBinary), cfg)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	srv := &http.Server{Handler: echoHandler(), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(sess) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = sess.Close()
		cancel()
	})
	return sess
}

// waitClosed reads the bare WebSocket until it fails and returns why. The
// gateway's yamux frames (keepalives) arrive as messages and are skipped.
func waitClosed(t *testing.T, ws *websocket.Conn) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("the connection was not closed")
			}
			return err
		}
	}
}
