// Package admission decides which requests may run on the engine (plan item
// B1, section 15.1). The count of requests in flight lives here and nowhere
// else: a Gate hands out Slots, and only releasing a Slot gives one back.
package admission

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Limit is how many requests may run on the engine at once. It is always at
// least 1: there is no "unlimited" (decision DC4).
type Limit struct{ n int32 }

// NewLimit refuses a limit below 1.
func NewLimit(n int) (Limit, error) {
	if n < 1 || n > 1<<20 {
		return Limit{}, fmt.Errorf("a concurrency limit must be from 1 to %d, not %d", 1<<20, n)
	}
	return Limit{n: int32(n)}, nil
}

// N is the limit as a count.
func (l Limit) N() int32 { return l.n }

// Refusal says why a request was not admitted.
type Refusal int

const (
	Admitted  Refusal = iota
	Busy              // every slot is taken
	Closed            // the gate is closed: a benchmark has the engine
	QueueFull         // too many returning turns are already waiting
	TimedOut          // a returning turn waited as long as it may
	Cancelled         // the request went away while it waited
)

func (r Refusal) String() string {
	switch r {
	case Admitted:
		return "admitted"
	case Busy:
		return "busy"
	case Closed:
		return "closed"
	case QueueFull:
		return "queue full"
	case TimedOut:
		return "timed out"
	case Cancelled:
		return "cancelled"
	default:
		return fmt.Sprintf("refusal(%d)", int(r))
	}
}

// Gate admits requests up to its limit.
type Gate struct {
	mu       sync.Mutex
	inflight int32
	limit    int32
	closed   bool
	waiters  []*waiter // returning turns, in arrival order
	onChange func()
}

type waiter struct {
	// ready receives the waiter's slot, or nil if the gate closed.
	ready chan *Slot
}

// NewGate makes an open gate with limit. onChange, if set, is called (not
// under a lock) whenever the count, the limit or the closed state changes.
func NewGate(limit Limit, onChange func()) *Gate {
	return &Gate{limit: limit.n, onChange: onChange}
}

// Slot is the right to run one request on the engine. Release gives it back;
// calling it again does nothing.
type Slot struct {
	g    *Gate
	once sync.Once
}

// Release gives the slot back to the gate, once.
func (s *Slot) Release() {
	if s == nil {
		return
	}
	s.once.Do(s.g.release)
}

// TryAcquire admits a new request if a slot is free and the gate is open.
func (g *Gate) TryAcquire() (*Slot, Refusal) {
	g.mu.Lock()
	switch {
	case g.closed:
		g.mu.Unlock()
		return nil, Closed
	case g.inflight >= g.limit:
		g.mu.Unlock()
		return nil, Busy
	}
	g.inflight++
	g.mu.Unlock()
	g.changed()
	return &Slot{g: g}, Admitted
}

// Acquire admits a returning turn of a conversation. With every slot taken
// it waits, in arrival order, behind at most maxWaiting other turns, for up
// to wait; a turn beyond that is refused at once. A release hands its slot
// straight to the first waiter.
func (g *Gate) Acquire(ctx context.Context, wait time.Duration, maxWaiting int) (*Slot, Refusal) {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil, Closed
	}
	if g.inflight < g.limit && len(g.waiters) == 0 {
		g.inflight++
		g.mu.Unlock()
		g.changed()
		return &Slot{g: g}, Admitted
	}
	if len(g.waiters) >= maxWaiting {
		g.mu.Unlock()
		return nil, QueueFull
	}
	w := &waiter{ready: make(chan *Slot, 1)}
	g.waiters = append(g.waiters, w)
	g.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case s := <-w.ready:
		if s == nil {
			return nil, Closed
		}
		return s, Admitted
	case <-ctx.Done():
		return g.abandon(w, Cancelled)
	case <-timer.C:
		return g.abandon(w, TimedOut)
	}
}

// abandon takes w out of the queue. If a slot reached it meanwhile, the slot
// goes back (to the next waiter, if any).
func (g *Gate) abandon(w *waiter, why Refusal) (*Slot, Refusal) {
	g.mu.Lock()
	for i, x := range g.waiters {
		if x == w {
			g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
			g.mu.Unlock()
			return nil, why
		}
	}
	g.mu.Unlock()
	if s := <-w.ready; s != nil {
		s.Release()
	}
	return nil, why
}

// ClaimIdle admits the client's own work (a capability probe) only when
// nothing else runs and the gate is open. The slot counts as in flight.
func (g *Gate) ClaimIdle() (*Slot, bool) {
	g.mu.Lock()
	if g.closed || g.inflight != 0 || len(g.waiters) != 0 {
		g.mu.Unlock()
		return nil, false
	}
	g.inflight++
	g.mu.Unlock()
	g.changed()
	return &Slot{g: g}, true
}

// SetLimit changes the limit. A higher limit admits waiting turns at once;
// a lower one takes effect as requests finish.
func (g *Gate) SetLimit(l Limit) {
	g.mu.Lock()
	g.limit = l.n
	g.grantLocked()
	g.mu.Unlock()
	g.changed()
}

// SetClosed closes or opens the gate. Closing it refuses every waiting turn
// at once, so the platform can send it elsewhere, and admits nothing more;
// requests already running finish.
func (g *Gate) SetClosed(closed bool) {
	g.mu.Lock()
	g.closed = closed
	if closed {
		for _, w := range g.waiters {
			w.ready <- nil
		}
		g.waiters = nil
	}
	g.mu.Unlock()
	g.changed()
}

// Snapshot is the gate's state, for the capacity report and the dashboard.
type Snapshot struct {
	Inflight  int32
	Limit     int32
	Waiting   int
	Accepting bool
}

// Snapshot reads the gate.
func (g *Gate) Snapshot() Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Snapshot{Inflight: g.inflight, Limit: g.limit, Waiting: len(g.waiters), Accepting: !g.closed}
}

func (g *Gate) release() {
	g.mu.Lock()
	g.inflight--
	g.grantLocked()
	g.mu.Unlock()
	g.changed()
}

// grantLocked hands free slots to waiters in arrival order. Call it with
// g.mu held.
func (g *Gate) grantLocked() {
	for !g.closed && g.inflight < g.limit && len(g.waiters) > 0 {
		w := g.waiters[0]
		g.waiters = g.waiters[1:]
		g.inflight++
		w.ready <- &Slot{g: g}
	}
}

func (g *Gate) changed() {
	if g.onChange != nil {
		g.onChange()
	}
}
