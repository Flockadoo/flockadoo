package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakeengine"
	"github.com/sentnl/inferoute-node/inferoute-client/pkg/llm"
)

// Plan item B1 (findings ADM-1 and ADM-3): a slot is released once however
// often its release is called, and returning turns of a conversation queue
// in arrival order, up to a limit.

// A second release of the same slot changes nothing: the engine still never
// runs more than the limit.
func TestASecondReleaseOfASlotChangesNothing(t *testing.T) {
	r := newRig(t, 1)
	release, ok := r.s.ClaimIdleSlot()
	if !ok {
		t.Fatal("could not claim the idle slot")
	}
	release()
	release()

	r.eng.Next(fakeengine.Stream("a").Stall(1), fakeengine.Stream("b").Stall(1))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	statuses := make(chan int, 2)
	for range 2 {
		go func() {
			resp, err := r.send(ctx, streamBody, nil)
			if err != nil {
				statuses <- 0
				return
			}
			statuses <- resp.StatusCode
			if resp.StatusCode == http.StatusOK {
				_, _ = io.Copy(io.Discard, resp.Body)
			}
			_ = resp.Body.Close()
		}()
	}
	// One of the two is refused; the other holds the only slot. Either may
	// answer first: the admitted one answers 200 as soon as the engine
	// streams, and both answer while the engine stalls.
	a, b := <-statuses, <-statuses
	if min(a, b) != http.StatusOK || max(a, b) != http.StatusServiceUnavailable {
		t.Fatalf("answers = %d and %d, want one request refused with 503 while the other runs", a, b)
	}
	if got := r.eng.MaxInFlight(); got > 1 {
		t.Fatalf("after a double release the engine ran %d requests at once with a limit of 1", got)
	}
	cancel()
	r.awaitSlotsFree(t, "after the double release")
}

// A panic in the engine call still returns the slot (7.3: failures do not
// leak slots). The relay's release is deferred; this pins it.
func TestAPanicInTheEngineCallReturnsTheSlot(t *testing.T) {
	r := newRig(t, 1)
	r.s.llmClient = panickingEngine{Client: r.s.llmClient}
	// The server aborts a response whose handler panicked, so the gateway
	// sees the stream end; either way it is not a success.
	if resp, err := r.send(context.Background(), streamBody, nil); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("a request whose engine call panicked was answered 200")
		}
	}
	r.awaitSlotsFree(t, "after a panic in the engine call")
}

// panickingEngine is an engine client with a bug: forwarding panics.
type panickingEngine struct{ llm.Client }

func (panickingEngine) ForwardRequest(context.Context, string, []byte, bool) (*http.Response, error) {
	panic("engine client bug")
}

// returningTurns sends n turns of one conversation while a first turn holds
// the only slot, each a few milliseconds after the last so their arrival
// order is known, and returns their statuses and the order the engine
// received them.
func returningTurns(t *testing.T, r *rig, n int) (statuses []int, order []int) {
	t.Helper()
	r.eng.Next(fakeengine.Stream("hold").Stall(1))
	holdCtx, release := context.WithCancel(context.Background())
	holder, err := r.send(holdCtx, streamBody, map[string]string{"X-Session-Key": "conversation"})
	if err != nil {
		t.Fatal(err)
	}
	r.eng.AwaitInFlight(t, 1)
	for range n {
		r.eng.Next(fakeengine.Stream("turn"))
	}

	statuses = make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := `{"model":"fake/model","stream":true,"turn":` + string(rune('0'+i)) + `}`
			resp, err := r.send(context.Background(), body, map[string]string{"X-Session-Key": "conversation"})
			if err != nil {
				return
			}
			statuses[i] = resp.StatusCode
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	release()
	_ = holder.Body.Close()
	wg.Wait()

	for _, req := range r.eng.Requests()[1:] {
		var b struct{ Turn int }
		if err := json.Unmarshal(req.Body, &b); err == nil {
			order = append(order, b.Turn)
		}
	}
	return statuses, order
}

func TestReturningTurnsAreAdmittedInArrivalOrder(t *testing.T) {
	r := newRig(t, 1)
	statuses, order := returningTurns(t, r, 6)
	for i, s := range statuses {
		if s != http.StatusOK {
			t.Fatalf("turn %d = %d, want 200 (all six fit the queue)", i, s)
		}
	}
	for i, turn := range order {
		if turn != i {
			t.Fatalf("the engine received the turns in the order %v, want arrival order", order)
		}
	}
}

func TestReturningTurnsBeyondTheQueueLimitAreRefusedAtOnce(t *testing.T) {
	r := newRig(t, 1)
	r.s.sessionQueueLimit = 2
	statuses, _ := returningTurns(t, r, 5)
	refused := 0
	for _, s := range statuses {
		if s == http.StatusServiceUnavailable {
			refused++
		}
	}
	if refused != 3 {
		t.Fatalf("statuses %v: %d refused, want the 3 beyond a queue of 2", statuses, refused)
	}
}
