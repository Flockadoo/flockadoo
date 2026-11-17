package connect

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakegateway"
)

// Plan item C1 (findings NET-2, NET-4, NET-5; decision DC8): the tunnel's
// lifecycle, one test per transition of section 6.4 that is wrong today.

func fastTiming() Timing {
	return Timing{BaseDelay: 10 * time.Millisecond, MaxDelay: 40 * time.Millisecond, Stable: time.Hour,
		KeepAlive: time.Hour, KeepAliveTimeout: time.Second, ReadHeaderTimeout: time.Second}
}

func runClient(t *testing.T, gw *fakegateway.Gateway, key string, timing Timing) *Client {
	t.Helper()
	c := New(gw.URL, key, http.NotFoundHandler(), nil)
	c.Timing = timing
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return c
}

func awaitState(t *testing.T, c *Client, want State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.State() != want {
		if time.Now().After(deadline) {
			t.Fatalf("state = %v (%q), want %v", c.State(), c.Reason(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// noMoreDials fails if the client dials again within a few reconnect delays.
func noMoreDials(t *testing.T, gw *fakegateway.Gateway) {
	t.Helper()
	n := len(gw.Dials())
	time.Sleep(300 * time.Millisecond)
	if m := len(gw.Dials()); m != n {
		t.Fatalf("a terminal client dialled again (%d dials, then %d)", n, m)
	}
}

// NET-2, DC8: a key rejected on a later dial is terminal: the client stops
// dialling and says why, and stays up.
func TestAKeyRejectedOnALaterDialIsTerminal(t *testing.T) {
	gw := fakegateway.New(t, "key")
	c := runClient(t, gw, "key", fastTiming())
	gw.AwaitSession(t)
	gw.RejectNext(http.StatusUnauthorized, "invalid API key")
	gw.Drop()
	awaitState(t, c, Rejected)
	if !c.State().Terminal() || !strings.Contains(c.Reason(), "rejected") {
		t.Fatalf("state %v, reason %q: want a terminal state that says the key was rejected", c.State(), c.Reason())
	}
	noMoreDials(t, gw)
}

// DC8: a session the gateway says was replaced by a newer one is terminal.
func TestAReplacedSessionIsTerminal(t *testing.T) {
	gw := fakegateway.New(t, "key")
	c := runClient(t, gw, "key", fastTiming())
	gw.AwaitSession(t)
	gw.ReplaceWithReason(CloseReplaced, "replaced by a newer session")
	awaitState(t, c, Replaced)
	if !strings.Contains(c.Reason(), "another") {
		t.Fatalf("reason %q does not say another client took over", c.Reason())
	}
	noMoreDials(t, gw)
}

// 6.4: a 426 on the dial means this version is too old: terminal, and says so.
func TestAnUpgradeRequiredIsTerminal(t *testing.T) {
	gw := fakegateway.New(t, "key")
	gw.RejectNext(http.StatusUpgradeRequired, "client upgrade required")
	c := runClient(t, gw, "key", fastTiming())
	awaitState(t, c, UpgradeRequired)
	if !strings.Contains(c.Reason(), "update") {
		t.Fatalf("reason %q does not say to update", c.Reason())
	}
	noMoreDials(t, gw)
}

// NET-4, DC8: until the gateway names a replacement, several short sessions
// in a row name the likely cause, and the client keeps trying.
func TestShortSessionsInARowNameTheLikelyCause(t *testing.T) {
	gw := fakegateway.New(t, "key")
	c := runClient(t, gw, "key", fastTiming())
	for i := 1; i <= 3; i++ {
		gw.AwaitDials(t, i)
		gw.AwaitSession(t)
		gw.Drop()
	}
	gw.AwaitDials(t, 4)
	if !strings.Contains(c.Reason(), "another client") {
		t.Fatalf("after three short sessions the reason is %q; want it to name another client using this key", c.Reason())
	}
	if c.State().Terminal() {
		t.Fatal("an inferred replacement stopped the client; only the gateway's word is final")
	}
}

// U2, 6.4: a gateway that stops answering without closing is found by the
// keepalive, and the client dials again.
func TestAHalfOpenGatewayIsFound(t *testing.T) {
	gw := fakegateway.New(t, "key")
	timing := fastTiming()
	timing.KeepAlive, timing.KeepAliveTimeout = 50*time.Millisecond, 100*time.Millisecond
	runClient(t, gw, "key", timing)
	gw.AwaitSession(t)
	start := time.Now()
	gw.Blackhole()
	gw.AwaitDials(t, 2)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the half-open session was found after %v", d)
	}
}

// NET-5: a stream that sends half a request header is closed within the
// header timeout.
func TestAStalledRequestHeaderIsClosed(t *testing.T) {
	gw := fakegateway.New(t, "key")
	timing := fastTiming()
	timing.ReadHeaderTimeout = 200 * time.Millisecond
	runClient(t, gw, "key", timing)
	gw.AwaitSession(t)
	stream, err := gw.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if _, err := stream.Write([]byte("POST /v1/chat/completions HTTP/1.1\r\nHost: provider\r\n")); err != nil {
		t.Fatal(err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	_, err = bufio.NewReader(stream).ReadString('\n')
	if err == nil {
		// net/http answers a timed-out header with 408 before closing.
		_, err = bufio.NewReader(stream).ReadString('\n')
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the stalled stream was still open after %v", time.Since(start))
	}
}
