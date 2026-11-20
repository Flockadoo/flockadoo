package verify

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Flockadoo/flockadoo/internal/testenv/fakeplatform"
)

// Plan item B3 (finding ADM-5, invariant I14, decision DC2): verification
// is state a background refresher keeps; the request path only reads it. A
// verified answer is used while fresh and, when the node cannot be asked,
// for a grace period after; an explicit negative refuses at once; a failure
// to reach the node is never recorded as a failed verification. The tests
// use the real clock with milliseconds: fresh for 100ms, grace 400ms more.

const (
	refreshAfter = 100 * time.Millisecond
	outageGrace  = 400 * time.Millisecond
)

type world struct {
	v    *Verifier
	plat *fakeplatform.Platform
	dir  string
}

func newWorld(t *testing.T, aliases ...string) *world {
	t.Helper()
	plat := fakeplatform.New(t, "k")
	var entries []map[string]any
	for i, a := range aliases {
		entries = append(entries, map[string]any{"id": "entry-" + string(rune('1'+i)), "alias": a, "service_type": "vllm", "is_active": true})
	}
	plat.Set(http.MethodGet, "/api/models/approved-builds", fakeplatform.JSON(http.StatusOK, map[string]any{"object": "list", "data": entries}))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"model_type":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "model.safetensors"), safetensors(`{"meta":{},"tensors":{}}`, []byte("w")), 0o600); err != nil {
		t.Fatal(err)
	}
	cat := NewCatalog(plat.URL, "vllm")
	if err := cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(cat, NewServerClient(plat.URL, "k"), "vllm", "", dir)
	v.SetTiming(refreshAfter, outageGrace)
	return &world{v: v, plat: plat, dir: dir}
}

// nodeSays makes every verify-model answer status from now on.
func (w *world) nodeSays(status string) {
	w.plat.Set(http.MethodPost, "/api/provider/verify-model", fakeplatform.JSON(http.StatusOK, map[string]string{"verification_status": status}))
}

// nodeDown makes every verify-model call fail as an outage does.
func (w *world) nodeDown() {
	w.plat.Set(http.MethodPost, "/api/provider/verify-model", fakeplatform.JSON(http.StatusBadGateway, map[string]string{"error": "upstream unavailable"}))
}

func (w *world) verifyCalls() int {
	return len(w.plat.Requests(http.MethodPost, "/api/provider/verify-model"))
}

func expectVerdict(t *testing.T, w *world, model string, want Verdict, when string) {
	t.Helper()
	if got, why := w.v.Verdict(model); got != want {
		t.Fatalf("%s: verdict for %s = %v (%s), want %v", when, model, got, why, want)
	}
}

// 1. A short outage does not interrupt a verified, unchanged model.
func TestAShortOutageDoesNotInterruptAVerifiedModel(t *testing.T) {
	w := newWorld(t, "m")
	w.nodeSays("verified")
	w.v.Refresh(context.Background(), "m")
	w.nodeDown()
	time.Sleep(2 * refreshAfter)
	w.v.Refresh(context.Background(), "m")
	expectVerdict(t, w, "m", Serve, "an outage inside the grace")
}

// 2. An outage that goes on ends in 503, never 403.
func TestAContinuingOutageEndsInUnavailable(t *testing.T) {
	w := newWorld(t, "m")
	w.nodeSays("verified")
	w.v.Refresh(context.Background(), "m")
	w.nodeDown()
	time.Sleep(refreshAfter + outageGrace/2)
	w.v.Refresh(context.Background(), "m")
	expectVerdict(t, w, "m", Serve, "before the grace runs out")
	time.Sleep(outageGrace)
	w.v.Refresh(context.Background(), "m")
	expectVerdict(t, w, "m", Unavailable, "after the grace runs out")
}

// 3. An explicit rejection blocks at once, with no grace.
func TestAnExplicitRejectionBlocksAtOnce(t *testing.T) {
	w := newWorld(t, "m")
	w.nodeSays("verified")
	w.v.Refresh(context.Background(), "m")
	time.Sleep(2 * refreshAfter)
	w.nodeSays("failed")
	w.v.Refresh(context.Background(), "m")
	expectVerdict(t, w, "m", Refuse, "the node said failed")
}

// 4. A new or changed model inherits nothing: during an outage, changed
// weight files and a second alias are unavailable, not refused.
func TestANewOrChangedModelInheritsNoVerdict(t *testing.T) {
	w := newWorld(t, "m", "m2")
	w.nodeSays("verified")
	w.v.Refresh(context.Background(), "m")
	w.nodeDown()
	expectVerdict(t, w, "m2", Unavailable, "a second alias never verified")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(w.dir, "model.safetensors"), later, later); err != nil {
		t.Fatal(err)
	}
	w.v.Refresh(context.Background(), "m")
	expectVerdict(t, w, "m", Unavailable, "changed weight files during an outage")
}

// 5. Recovery restores the state without a restart.
func TestRecoveryRestoresTheVerdict(t *testing.T) {
	w := newWorld(t, "m")
	w.nodeDown()
	w.v.Refresh(context.Background(), "m")
	expectVerdict(t, w, "m", Unavailable, "never verified, node down")
	w.nodeSays("verified")
	w.v.Refresh(context.Background(), "m")
	expectVerdict(t, w, "m", Serve, "the node is back and says verified")
	time.Sleep(2 * refreshAfter)
	w.nodeSays("failed")
	w.v.Refresh(context.Background(), "m")
	expectVerdict(t, w, "m", Refuse, "the node is back and says failed")
}

// 6. A failure to reach the node is never recorded as a failed
// verification, in the verdict or in the health report's status.
func TestAnOutageIsNeverRecordedAsFailed(t *testing.T) {
	w := newWorld(t, "m")
	w.nodeSays("verified")
	w.v.Refresh(context.Background(), "m")
	w.nodeDown()
	time.Sleep(2 * refreshAfter)
	w.v.Refresh(context.Background(), "m")
	if st := w.v.Status("m"); st == StatusFailed {
		t.Fatalf("the status reported to the node is %q during an outage", st)
	}
	if _, why := w.v.Verdict("m"); why == string(StatusFailed) {
		t.Fatalf("the verdict's reason is %q during an outage", why)
	}
}

// 7. Never verified, and the node cannot be asked: 503.
func TestNeverVerifiedDuringAnOutageIsUnavailable(t *testing.T) {
	w := newWorld(t, "m")
	w.nodeDown()
	w.v.Refresh(context.Background(), "m")
	expectVerdict(t, w, "m", Unavailable, "never verified, node down")
}

// 8. A request with a verdict is not held up by a refresh in progress.
func TestARefreshInProgressDoesNotDelayARequest(t *testing.T) {
	w := newWorld(t, "m")
	w.nodeSays("verified")
	w.v.Refresh(context.Background(), "m")
	time.Sleep(2 * refreshAfter)
	// The node hangs for the refresh, and for anything else that asks.
	w.plat.Next(http.MethodPost, "/api/provider/verify-model", fakeplatform.Hang(), fakeplatform.Hang())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.v.Refresh(ctx, "m")
	w.plat.AwaitRequests(t, http.MethodPost, "/api/provider/verify-model", 2)
	done := make(chan struct{})
	go func() { w.v.Verdict("m"); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a request waited on the node while a refresh was in progress")
	}
}

// 9. No inference request calls verify-model: only the refresher does.
func TestRequestsMakeNoVerifyCalls(t *testing.T) {
	w := newWorld(t, "m")
	w.nodeSays("verified")
	w.v.Refresh(context.Background(), "m")
	before := w.verifyCalls()
	time.Sleep(2 * refreshAfter)
	for range 100 {
		w.v.Verdict("m")
	}
	if n := w.verifyCalls() - before; n != 0 {
		t.Fatalf("100 requests made %d verify-model calls", n)
	}
}
