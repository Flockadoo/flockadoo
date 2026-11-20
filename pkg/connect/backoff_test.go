package connect

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Flockadoo/flockadoo/internal/testenv/fakegateway"
)

// Plan item Q1, finding NET-1: the reconnect delay returns to its base only
// after a session has stayed connected for the stability period. A session
// that ends sooner leaves the delay growing, so two clients that share a key
// cannot replace each other at full speed (NET-4).
//
// The tests read the delay Run chose before each dial, where it waits for
// it, and check it against the rule: the delay, plus up to half of it as
// jitter. They do not time the gap from a drop to the next dial: that gap
// also holds however long the machine takes to notice the drop, which on a
// loaded CI runner was as long as the delay itself (100 ms on macOS) and made
// any bound on the gap flaky. The client still waits each delay in full.

// waits is what a client chose before each redial, in order.
type waits struct {
	mu sync.Mutex
	d  []time.Duration
}

func (w *waits) all() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.d)
}

// runWithTiming runs the real client against gw with the given pacing, and
// records each delay it waits before dialling again.
func runWithTiming(t *testing.T, gw *fakegateway.Gateway, timing Timing) *waits {
	t.Helper()
	w := &waits{}
	c := New(gw.URL, "key", http.NotFoundHandler(), nil)
	c.Timing = timing
	c.sleep = func(ctx context.Context, d time.Duration) bool {
		w.mu.Lock()
		w.d = append(w.d, d)
		w.mu.Unlock()
		return sleep(ctx, d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return w
}

// dropAfter waits for dial n to become a session, holds it for hold, drops
// it, waits for the next dial, and returns the delay the client chose before
// that dial.
func dropAfter(t *testing.T, gw *fakegateway.Gateway, w *waits, n int, hold time.Duration) time.Duration {
	t.Helper()
	gw.AwaitDials(t, n)
	gw.AwaitSession(t)
	time.Sleep(hold)
	gw.Drop()
	gw.AwaitDials(t, n+1)
	got := w.all()
	if len(got) != n {
		t.Fatalf("dial %d came after %d waits, want %d: %v", n+1, len(got), n, got)
	}
	return got[n-1]
}

// wantDelay fails unless d is the delay want plus its jitter, at most half of
// want.
func wantDelay(t *testing.T, what string, d, want time.Duration) {
	t.Helper()
	if d < want || d > want+want/2 {
		t.Fatalf("%s: waited %v, want %v plus at most %v of jitter", what, d, want, want/2)
	}
}

// The stability period and the holds keep a "short" session clearly short
// and a "stable" one clearly stable on a slow runner: a short one is held 0
// and lasts only as long as noticing the drop takes, a stable one is held
// well past the period.
const (
	testStable     = 300 * time.Millisecond
	testStableHold = 500 * time.Millisecond
)

// 1. Stable sessions: each lasts past the stability period, so every
// reconnect waits only the base delay and its jitter.
func TestAfterAStableSessionTheDelayIsTheBase(t *testing.T) {
	gw := fakegateway.New(t, "key")
	timing := Timing{BaseDelay: 10 * time.Millisecond, MaxDelay: 32 * 10 * time.Millisecond, Stable: testStable}
	w := runWithTiming(t, gw, timing)
	for i := 1; i <= 4; i++ {
		d := dropAfter(t, gw, w, i, testStableHold)
		wantDelay(t, fmt.Sprintf("drop %d after a stable session", i), d, timing.BaseDelay)
	}
}

// 2. Short sessions: each ends before the stability period, so the delay
// keeps doubling to its cap and stays there. This holds today and must keep
// holding: it is what stops a reset on every connect.
func TestShortSessionsLeaveTheDelayGrowing(t *testing.T) {
	gw := fakegateway.New(t, "key")
	timing := Timing{BaseDelay: 10 * time.Millisecond, MaxDelay: 80 * time.Millisecond, Stable: time.Hour}
	w := runWithTiming(t, gw, timing)
	want := timing.BaseDelay
	for i := 1; i <= 6; i++ {
		d := dropAfter(t, gw, w, i, 0)
		wantDelay(t, fmt.Sprintf("drop %d after a short session", i), d, want)
		want = min(want*2, timing.MaxDelay)
	}
}

// 3. Several short sessions, then one stable one: the next delay is the base.
func TestAStableSessionAfterShortOnesResetsTheDelay(t *testing.T) {
	gw := fakegateway.New(t, "key")
	timing := Timing{BaseDelay: 10 * time.Millisecond, MaxDelay: 32 * 10 * time.Millisecond, Stable: testStable}
	w := runWithTiming(t, gw, timing)
	want := timing.BaseDelay
	for i := 1; i <= 4; i++ {
		d := dropAfter(t, gw, w, i, 0)
		wantDelay(t, fmt.Sprintf("short session %d", i), d, want)
		want = min(want*2, timing.MaxDelay)
	}
	d := dropAfter(t, gw, w, 5, testStableHold)
	wantDelay(t, "after a stable session that followed four short ones", d, timing.BaseDelay)
}

// 4. The default stability period is longer than the longest delay with its
// jitter. Otherwise two clients sharing a key, each connected only while the
// other waits, would count as stable and replace each other at full speed.
func TestTheDefaultStabilityPeriodOutlastsTheLongestDelay(t *testing.T) {
	d := DefaultTiming()
	longest := d.MaxDelay + d.MaxDelay/2
	if d.Stable <= longest {
		t.Fatalf("stability period %v is not longer than the longest delay %v", d.Stable, longest)
	}
	if d.BaseDelay != time.Second || d.MaxDelay != 32*time.Second || d.Stable != 60*time.Second {
		t.Fatalf("defaults = %+v, want 1s, 32s and 60s", d)
	}
}
