package admission

import (
	"context"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func limit(t *testing.T, n int) Limit {
	t.Helper()
	l, err := NewLimit(n)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// Decision DC4: a limit is at least 1.
func TestNewLimitRefusesBelowOne(t *testing.T) {
	for _, n := range []int{0, -1, -1 << 40} {
		if _, err := NewLimit(n); err == nil {
			t.Errorf("NewLimit(%d) was accepted", n)
		}
	}
	if l, err := NewLimit(4); err != nil || l.N() != 4 {
		t.Fatalf("NewLimit(4) = %v, %v", l, err)
	}
}

// Moved from pkg/server (TestTryAcquireInference, TestIsBusyIsSlotOnly).
func TestTryAcquireAdmitsUpToTheLimit(t *testing.T) {
	g := NewGate(limit(t, 1), nil)
	s, why := g.TryAcquire()
	if s == nil || why != Admitted {
		t.Fatalf("first acquire = %v", why)
	}
	if _, why := g.TryAcquire(); why != Busy {
		t.Fatalf("second acquire = %v, want busy", why)
	}
	s.Release()
	if s2, why := g.TryAcquire(); s2 == nil {
		t.Fatalf("acquire after release = %v", why)
	}
}

// Finding ADM-1: a second release changes nothing.
func TestASlotIsReleasedOnce(t *testing.T) {
	g := NewGate(limit(t, 1), nil)
	a, _ := g.TryAcquire()
	a.Release()
	a.Release()
	b, _ := g.TryAcquire()
	if b == nil {
		t.Fatal("acquire after a release failed")
	}
	if _, why := g.TryAcquire(); why != Busy {
		t.Fatalf("after a double release a second request was %v with a limit of 1", why)
	}
	if n := g.Snapshot().Inflight; n != 1 {
		t.Fatalf("inflight = %d, want 1", n)
	}
}

// Moved from pkg/server (TestClaimIdleSlot): the client's own work runs only
// when nothing else does, and counts as in flight.
func TestClaimIdle(t *testing.T) {
	g := NewGate(limit(t, 4), nil)
	probe, ok := g.ClaimIdle()
	if !ok {
		t.Fatal("claim refused on an idle gate")
	}
	if _, ok := g.ClaimIdle(); ok {
		t.Fatal("second claim granted while the first is held")
	}
	if g.Snapshot().Inflight != 1 {
		t.Fatal("a claim does not count as in flight")
	}
	probe.Release()

	req, _ := g.TryAcquire()
	if _, ok := g.ClaimIdle(); ok {
		t.Fatal("claim granted beside a request in flight")
	}
	req.Release()

	g.SetClosed(true)
	if _, ok := g.ClaimIdle(); ok {
		t.Fatal("claim granted while the gate is closed")
	}
	if n := g.Snapshot().Inflight; n != 0 {
		t.Fatalf("a refused claim left inflight = %d", n)
	}
}

// Moved from pkg/server (TestRaisingLimitUnblocksQueuedSession).
func TestRaisingTheLimitAdmitsAWaitingTurn(t *testing.T) {
	g := NewGate(limit(t, 1), nil)
	held, _ := g.TryAcquire()
	defer held.Release()
	done := make(chan Refusal, 1)
	go func() {
		s, why := g.Acquire(context.Background(), 2*time.Second, 8)
		s.Release()
		done <- why
	}()
	waitFor(t, func() bool { return g.Snapshot().Waiting == 1 })
	g.SetLimit(limit(t, 2))
	select {
	case why := <-done:
		if why != Admitted {
			t.Fatalf("waiting turn = %v after the limit rose", why)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting turn did not notice the new limit")
	}
}

// Finding ADM-3: waiting turns are admitted in arrival order, woken by a
// release, and beyond the queue limit a turn is refused at once.
func TestWaitingTurnsAreAdmittedInArrivalOrder(t *testing.T) {
	g := NewGate(limit(t, 1), nil)
	held, _ := g.TryAcquire()
	const n = 8
	order := make(chan int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, why := g.Acquire(context.Background(), 5*time.Second, n)
			if why != Admitted {
				t.Errorf("turn %d = %v", i, why)
				return
			}
			order <- i
			s.Release()
		}()
		waitFor(t, func() bool { return g.Snapshot().Waiting == i+1 })
	}
	held.Release()
	wg.Wait()
	close(order)
	want := 0
	for got := range order {
		if got != want {
			t.Fatalf("turn %d admitted when turn %d was next", got, want)
		}
		want++
	}
}

func TestATurnBeyondTheQueueIsRefusedAtOnce(t *testing.T) {
	g := NewGate(limit(t, 1), nil)
	held, _ := g.TryAcquire()
	defer held.Release()
	for i := range 2 {
		go func() { _, _ = g.Acquire(context.Background(), 5*time.Second, 2) }()
		waitFor(t, func() bool { return g.Snapshot().Waiting == i+1 })
	}
	start := time.Now()
	if _, why := g.Acquire(context.Background(), 5*time.Second, 2); why != QueueFull {
		t.Fatalf("third waiter = %v, want queue full", why)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the refusal was not at once")
	}
}

// A turn that gives up, by cancellation or timeout, leaves the queue and
// takes no slot with it.
func TestAWaitingTurnThatGivesUpLeavesNoTrace(t *testing.T) {
	g := NewGate(limit(t, 1), nil)
	held, _ := g.TryAcquire()
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan Refusal, 1)
	go func() { _, why := g.Acquire(ctx, 5*time.Second, 4); cancelled <- why }()
	waitFor(t, func() bool { return g.Snapshot().Waiting == 1 })
	cancel()
	if why := <-cancelled; why != Cancelled {
		t.Fatalf("cancelled turn = %v", why)
	}
	if _, why := g.Acquire(context.Background(), 20*time.Millisecond, 4); why != TimedOut {
		t.Fatalf("turn that waited out = %v", why)
	}
	held.Release()
	if s := g.Snapshot(); s.Inflight != 0 || s.Waiting != 0 {
		t.Fatalf("after both gave up: %+v", s)
	}
}

// Invariant I11: closing the gate for a benchmark admits nothing and refuses
// the waiting turns at once.
func TestClosingTheGateRefusesEveryWaitingTurn(t *testing.T) {
	g := NewGate(limit(t, 1), nil)
	held, _ := g.TryAcquire()
	got := make(chan Refusal, 1)
	go func() { _, why := g.Acquire(context.Background(), 5*time.Second, 4); got <- why }()
	waitFor(t, func() bool { return g.Snapshot().Waiting == 1 })
	g.SetClosed(true)
	if why := <-got; why != Closed {
		t.Fatalf("waiting turn = %v when the gate closed", why)
	}
	held.Release()
	if _, why := g.TryAcquire(); why != Closed {
		t.Fatalf("new request = %v while closed", why)
	}
	if s := g.Snapshot(); s.Accepting || s.Inflight != 0 {
		t.Fatalf("closed gate: %+v", s)
	}
	g.SetClosed(false)
	if s, why := g.TryAcquire(); s == nil {
		t.Fatalf("after reopening: %v", why)
	}
}

// The property test of finding ADM-1 and invariants I1 and I2: under random
// acquires, double releases, cancellations and panics, the work running at
// once never exceeds the limit, and the count ends at zero.
func TestTheLimitHoldsUnderRandomUse(t *testing.T) {
	const lim = 3
	g := NewGate(limit(t, lim), nil)
	var running, highWater atomic.Int32
	work := func() {
		n := running.Add(1)
		for {
			h := highWater.Load()
			if n <= h || highWater.CompareAndSwap(h, n) {
				break
			}
		}
		time.Sleep(time.Duration(rand.IntN(200)) * time.Microsecond)
		running.Add(-1)
	}
	var wg sync.WaitGroup
	for i := range 400 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = recover() }()
			var s *Slot
			switch i % 4 {
			case 0, 1:
				s, _ = g.TryAcquire()
			case 2:
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(rand.IntN(3))*time.Millisecond)
				s, _ = g.Acquire(ctx, 50*time.Millisecond, 16)
				cancel()
			case 3:
				s, _ = g.ClaimIdle()
			}
			if s == nil {
				return
			}
			defer s.Release()
			work()
			if rand.IntN(4) == 0 {
				s.Release() // a double release, with the deferred one
			}
			if rand.IntN(10) == 0 {
				panic("a bug between claim and release")
			}
		}()
	}
	wg.Wait()
	if h := highWater.Load(); h > lim {
		t.Fatalf("%d requests ran at once with a limit of %d", h, lim)
	}
	if s := g.Snapshot(); s.Inflight != 0 || s.Waiting != 0 {
		t.Fatalf("after every request finished: %+v", s)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(time.Millisecond)
	}
}
