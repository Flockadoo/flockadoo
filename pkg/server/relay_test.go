package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/testenv/fakeengine"
)

// Plan item B2 (findings STR-1, STR-3, STR-5, NET-5; invariants I4, I5, I6).

// outcomes is the recorded outcome of every request, in order.
func (r *rig) outcomes() []string {
	r.s.requestStats.mutex.Lock()
	defer r.s.requestStats.mutex.Unlock()
	var out []string
	for _, e := range r.s.requestStats.LastRequests {
		out = append(out, e.Outcome)
	}
	return out
}

// awaitOutcomes waits until n more outcomes than before have been recorded
// and returns the new ones, sorted.
func (r *rig) awaitOutcomes(t *testing.T, before, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := r.outcomes()
		if len(got) >= before+n {
			if len(got) > before+n {
				t.Fatalf("%d requests recorded %d outcomes: %q", n, len(got)-before, got[before:])
			}
			out := slices.Clone(got[before:])
			slices.Sort(out)
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d outcomes, have %q", n, got[before:])
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// I4: an engine that stops sending with the stream open frees its slot
// within the idle limit, and its request is cancelled.
func TestAStalledEngineFreesItsSlotWithinTheIdleLimit(t *testing.T) {
	r := newRig(t, 1)
	r.s.engineIdleLimit = 300 * time.Millisecond
	r.eng.Next(fakeengine.Stream("a", "b", "c").Stall(3))
	resp, err := r.send(context.Background(), streamBody, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	start := time.Now()
	r.eng.AwaitIdle(t)
	r.awaitSlotsFree(t, "after the engine stalled")
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the slot came back after %v with an idle limit of 300ms", d)
	}
	if reqs := r.eng.Requests(); !reqs[0].Cancelled {
		t.Fatal("the stalled engine request was not cancelled")
	}
}

// I6: a stream the engine cuts is not handed on as complete: the gateway's
// read fails instead of ending cleanly.
func TestAnEngineFailureMidStreamIsNotPresentedAsComplete(t *testing.T) {
	r := newRig(t, 1)
	r.eng.Next(fakeengine.Stream("a", "b", "c").Partial(1))
	resp, err := r.send(context.Background(), streamBody, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, err = io.ReadAll(resp.Body)
	if err == nil {
		t.Fatal("a stream the engine cut reached the gateway as a complete response")
	}
}

// I5: every request ends with exactly one recorded outcome, which says how.
func TestEveryRequestRecordsOneOutcome(t *testing.T) {
	r := newRig(t, 1)
	r.s.engineIdleLimit = 300 * time.Millisecond
	check := func(name string, want []string, run func()) {
		t.Helper()
		before := len(r.outcomes())
		run()
		if got := r.awaitOutcomes(t, before, len(want)); !slices.Equal(got, want) {
			t.Fatalf("%s: outcomes %q, want %q", name, got, want)
		}
	}
	check("a stream that completes", []string{"completed"}, func() {
		r.eng.Next(fakeengine.Stream("a"))
		r.call(t, streamBody, nil)
	})
	check("a model this machine does not serve", []string{"refused_model"}, func() {
		r.call(t, `{"model":"other","stream":true}`, nil)
	})
	check("a body that is not JSON", []string{"refused_bad_request"}, func() {
		r.call(t, `model=fake/model`, nil)
	})
	check("a body over the limit", []string{"refused_too_large"}, func() {
		r.call(t, `{"model":"fake/model","pad":"`+strings.Repeat("x", maxRequestBody)+`"}`, nil)
	})
	check("a stream the engine cuts", []string{"engine_failed"}, func() {
		r.eng.Next(fakeengine.Stream("a", "b").Partial(1))
		r.call(t, streamBody, nil)
	})
	check("an engine that stalls", []string{"stalled"}, func() {
		r.eng.Next(fakeengine.Stream("a").Stall(1))
		r.call(t, streamBody, nil)
	})
	check("a busy engine, and a gateway that gives up", []string{"consumer_gone", "refused_busy"}, func() {
		r.s.engineIdleLimit = time.Minute
		r.eng.Next(fakeengine.Stream("a").Stall(1))
		ctx, cancel := context.WithCancel(context.Background())
		held, err := r.send(ctx, streamBody, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = bufio.NewReader(held.Body).ReadString('\n')
		r.call(t, streamBody, nil)
		cancel()
		_ = held.Body.Close()
	})
	check("an engine that is gone", []string{"engine_unreachable"}, func() {
		r.eng.Next(fakeengine.Crash())
		r.call(t, streamBody, nil)
		r.call(t, streamBody, nil)
	})
}

// NET-5, 7.3: a body over the limit is refused before the engine.
func TestABodyOverTheLimitIsRefusedBeforeTheEngine(t *testing.T) {
	r := newRig(t, 1)
	status, _ := r.call(t, `{"model":"fake/model","pad":"`+strings.Repeat("x", maxRequestBody)+`"}`, nil)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", status)
	}
	if n := len(r.eng.Requests()); n != 0 {
		t.Fatalf("the engine received %d requests", n)
	}
}

// STR-5: every recording in the corpus crosses the tunnel and the relay byte
// for byte. Passes before and after the relay changes.
func TestEveryRecordingCrossesTheRelayByteForByte(t *testing.T) {
	recordings, err := filepath.Glob(filepath.Join(fakeengine.CorpusDir(), "*", "*.sse"))
	if err != nil || len(recordings) == 0 {
		t.Fatalf("no recordings: %v", err)
	}
	r := newRig(t, 1)
	for _, path := range recordings {
		rel, _ := filepath.Rel(fakeengine.CorpusDir(), strings.TrimSuffix(path, ".sse"))
		rec := filepath.ToSlash(rel)
		t.Run(rec, func(t *testing.T) {
			r.eng.Next(fakeengine.Replay(t, rec))
			resp, err := r.send(context.Background(), streamBody, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil && !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			want, _ := os.ReadFile(path)
			if string(got) != string(want) {
				t.Fatalf("%d bytes came back, the recording has %d", len(got), len(want))
			}
		})
	}
}
