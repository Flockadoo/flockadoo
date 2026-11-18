package server

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakeengine"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/capacity"
)

// Plan item C2 (findings NET-3, NET-7, ENG-5; decisions DC5, DC9;
// invariant I7).

// withCapacity wires a capacity sender to the rig, as main does.
func withCapacity(t *testing.T, r *rig) *capacity.Sender {
	t.Helper()
	sender := capacity.NewSender(r.plat.URL, rigKey, r.s.SlotSnapshot, nil)
	sender.Active = r.s.TunnelActive
	r.s.SetCapacityNotify(sender.Notify)
	r.s.capacity = sender
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go sender.Loop(ctx)
	return sender
}

// awaitAccepting waits for a capacity report that says accepting is want.
func awaitAccepting(t *testing.T, r *rig, want bool) {
	t.Helper()
	needle := `"accepting":false`
	if want {
		needle = `"accepting":true`
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		posts := r.plat.Requests(http.MethodPost, "/api/provider/capacity")
		if len(posts) > 0 && strings.Contains(string(posts[len(posts)-1].Body), needle) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no capacity report with %s", needle)
}

// NET-7, I7, DC5: an engine that stops answering is reported as not
// accepting, requests are refused without an engine call, and when it
// answers again the client accepts again. It is not restarted.
func TestADeadEngineIsReportedNotAcceptingUntilItIsBack(t *testing.T) {
	r := newRig(t, 2)
	withCapacity(t, r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.s.WatchEngine(ctx, 30*time.Millisecond)

	r.eng.SetReady(false)
	awaitAccepting(t, r, false)
	before := len(r.eng.Requests())
	if status, _ := r.call(t, streamBody, nil); status != http.StatusServiceUnavailable {
		t.Fatalf("a request while the engine is down = %d, want 503", status)
	}
	if len(r.eng.Requests()) != before {
		t.Fatal("a request reached an engine that is down")
	}
	r.eng.SetReady(true)
	awaitAccepting(t, r, true)
}

// NET-3, DC9: a stream in flight when the client stops is let finish; a
// not-accepting report goes out first, and a new request is refused.
func TestAStreamInFlightAtStopFinishes(t *testing.T) {
	r := newRig(t, 1)
	withCapacity(t, r)
	r.eng.Next(fakeengine.Stream("a", "b", "c", "d").Slow(100 * time.Millisecond))
	resp, err := r.send(context.Background(), streamBody, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	br := bufio.NewReader(resp.Body)
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	drained := make(chan error, 1)
	go func() { drained <- r.s.Drain(context.Background(), 5*time.Second) }()

	awaitAccepting(t, r, false)
	if status, _ := r.call(t, streamBody, nil); status != http.StatusServiceUnavailable {
		t.Fatalf("a request during the drain = %d, want 503", status)
	}
	rest, err := io.ReadAll(br)
	if err != nil || !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("the stream in flight did not finish: %v, %q", err, rest)
	}
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the drain did not end when the stream did")
	}
}

// DC9: the drain waits no longer than its limit.
func TestTheDrainEndsAtItsLimit(t *testing.T) {
	r := newRig(t, 1)
	r.eng.Next(fakeengine.Stream("a").Stall(1))
	resp, err := r.send(context.Background(), streamBody, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	r.eng.AwaitInFlight(t, 1)
	start := time.Now()
	_ = r.s.Drain(context.Background(), 200*time.Millisecond)
	if d := time.Since(start); d < 200*time.Millisecond || d > 2*time.Second {
		t.Fatalf("the drain took %v with a limit of 200ms and a stream that never ends", d)
	}
}

// DC9: a second stop request ends the drain at once.
func TestASecondStopEndsTheDrainAtOnce(t *testing.T) {
	r := newRig(t, 1)
	r.eng.Next(fakeengine.Stream("a").Stall(1))
	resp, err := r.send(context.Background(), streamBody, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	r.eng.AwaitInFlight(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	drained := make(chan struct{})
	go func() {
		_ = r.s.Drain(ctx, 10*time.Second)
		close(drained)
	}()
	time.Sleep(50 * time.Millisecond)
	select {
	case <-drained:
		t.Fatal("the drain ended before the stream did and before its limit")
	default:
	}
	if _, _, accepting := r.s.SlotSnapshot(); accepting {
		t.Fatal("still accepting during the drain")
	}
	cancel()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("the drain went on after the second stop")
	}
}

// I7, 6.4: only a connected tunnel may say accepting.
func TestOnlyAConnectedTunnelSaysAccepting(t *testing.T) {
	r := newRig(t, 1)
	// The gateway sees the session a moment before the client marks itself
	// connected, so wait for it.
	awaitSnapshotAccepting(t, r, true, "not accepting while connected")
	r.gw.RejectNext(http.StatusServiceUnavailable, "draining")
	r.gw.Drop()
	awaitSnapshotAccepting(t, r, false, "still accepting with the tunnel down")
}

func awaitSnapshotAccepting(t *testing.T, r *rig, want bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, accepting := r.s.SlotSnapshot(); accepting == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
