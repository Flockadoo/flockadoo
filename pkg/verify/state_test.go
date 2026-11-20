package verify

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Flockadoo/flockadoo/internal/testenv/fakeplatform"
	"github.com/Flockadoo/flockadoo/pkg/llm"
)

// Plan item B3 (finding ADM-5, invariant I14, decision DC2): verification
// is state a background refresher keeps; the request path only reads it. A
// verified answer is used while fresh and, when the server cannot be asked,
// for a grace period after; an explicit negative refuses at once; a failure
// to reach the server is never recorded as a failed verification. The tests
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
	expectVerdict(t, w, "m", Refuse, "the server said failed")
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
	expectVerdict(t, w, "m", Serve, "the server is back and says verified")
	time.Sleep(2 * refreshAfter)
	w.nodeSays("failed")
	w.v.Refresh(context.Background(), "m")
	expectVerdict(t, w, "m", Refuse, "the server is back and says failed")
}

// 6. A failure to reach the server is never recorded as a failed
// verification, in the verdict or in the health report's status.
func TestAnOutageIsNeverRecordedAsFailed(t *testing.T) {
	w := newWorld(t, "m")
	w.nodeSays("verified")
	w.v.Refresh(context.Background(), "m")
	w.nodeDown()
	time.Sleep(2 * refreshAfter)
	w.v.Refresh(context.Background(), "m")
	if st := w.v.Status("m"); st == StatusFailed {
		t.Fatalf("the status reported to the server is %q during an outage", st)
	}
	if _, why := w.v.Verdict("m"); why == string(StatusFailed) {
		t.Fatalf("the verdict's reason is %q during an outage", why)
	}
}

// 7. Never verified, and the server cannot be asked: 503.
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
	// The server hangs for the refresh, and for anything else that asks.
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
		t.Fatal("a request waited on the server while a refresh was in progress")
	}
}

// reportModel is what the health report says about model right now.
func reportModel(ctx context.Context, w *world, model string) llm.Model {
	// The test catalog has no max_model_len, so the engine is never asked.
	return w.v.ApplyToModels(ctx, nil, []llm.Model{{ID: model}})[0]
}

// 10. The first report after a restart carries the answer of the refresh
// already in progress. Without it the report went out with no fingerprint,
// the server judged the model failed, and the machine was red until the next
// report three minutes later (seen on the Mac mini, 2026-11-20).
func TestTheFirstReportCarriesTheAnswerOfARefreshInProgress(t *testing.T) {
	w := newWorld(t, "m")
	w.plat.Next(http.MethodPost, "/api/provider/verify-model", fakeplatform.Delayed(300*time.Millisecond,
		fakeplatform.JSON(http.StatusOK, map[string]string{"verification_status": "verified", "weight_fingerprint": "abc"})))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.v.Refresh(ctx, "m") // the background refresher, started with the report
	w.plat.AwaitRequests(t, http.MethodPost, "/api/provider/verify-model", 1)
	got := reportModel(ctx, w, "m")
	if got.VerificationStatus != string(StatusVerified) || got.WeightFingerprint != "abc" {
		t.Fatalf("the first report says status %q, fingerprint %q; want %q and %q, the answer of the refresh in progress",
			got.VerificationStatus, got.WeightFingerprint, StatusVerified, "abc")
	}
}

// 11. A report that already has an answer does not wait for a refresh in
// progress: a slow or unreachable server never holds up the reports.
func TestAReportWithAnAnswerDoesNotWaitForARefresh(t *testing.T) {
	w := newWorld(t, "m")
	w.nodeSays("verified")
	w.v.Refresh(context.Background(), "m")
	time.Sleep(2 * refreshAfter)
	w.plat.Next(http.MethodPost, "/api/provider/verify-model", fakeplatform.Hang(), fakeplatform.Hang())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.v.Refresh(ctx, "m")
	w.plat.AwaitRequests(t, http.MethodPost, "/api/provider/verify-model", 2)
	done := make(chan llm.Model, 1)
	go func() { done <- reportModel(ctx, w, "m") }()
	select {
	case got := <-done:
		if got.VerificationStatus != string(StatusVerified) {
			t.Fatalf("the report says %q, want the last answer %q", got.VerificationStatus, StatusVerified)
		}
	case <-time.After(time.Second):
		t.Fatal("a report with an answer waited on a refresh in progress")
	}
}

// 12. The wait for a first answer ends with the report's context, and a
// report with no answer never says failed (invariant I14).
func TestTheFirstReportWaitsNoLongerThanItsContext(t *testing.T) {
	w := newWorld(t, "m")
	w.plat.Next(http.MethodPost, "/api/provider/verify-model", fakeplatform.Hang(), fakeplatform.Hang())
	refreshCtx, cancelRefresh := context.WithCancel(context.Background())
	defer cancelRefresh()
	go w.v.Refresh(refreshCtx, "m")
	w.plat.AwaitRequests(t, http.MethodPost, "/api/provider/verify-model", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan llm.Model, 1)
	go func() { done <- reportModel(ctx, w, "m") }()
	select {
	case got := <-done:
		if got.VerificationStatus == string(StatusFailed) {
			t.Fatalf("a report with no answer says %q", got.VerificationStatus)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the report kept waiting after its context ended")
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
