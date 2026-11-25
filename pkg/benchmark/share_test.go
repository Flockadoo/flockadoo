package benchmark

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// Engine start memory plan, decision D2 (policy). The benchmark measures how
// many requests at once the engine handles, and that depends on vLLM's request
// cache, so on the memory share it was started with (E2). The result records
// the share it was measured at. A start with a share lower by more than 0.05
// marks the result stale: the benchmark runs again, and until it has, the
// limit is scaled down by the same ratio. A share that is the same, a little
// lower, higher, or not known keeps the cached result.

type shareRun struct {
	applied  []int
	notes    []string
	requests atomic.Int32
}

// shareWatcher is a ready engine with a cached result of concurrency 8
// measured at share measured, now started at share now. The engine fails every
// request, so a measurement ends at the fallback of 4.
func shareWatcher(t *testing.T, measured, now float64) (*Watcher, *shareRun) {
	t.Helper()
	run := &shareRun{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		run.requests.Add(1)
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := save(cfg, Result{Method: countMethod, Fingerprint: "fp", Concurrency: 8, MemoryShare: measured}); err != nil {
		t.Fatal(err)
	}
	w := &Watcher{
		ConfigPath:  cfg,
		EngineURL:   srv.URL,
		Model:       "m",
		client:      srv.Client(),
		Ready:       func() bool { return true },
		Fingerprint: func() string { return "fp" },
		Apply:       func(n int) { run.applied = append(run.applied, n) },
		Note:        func(s string) { run.notes = append(run.notes, s) },
		Share:       func() float64 { return now },
	}
	return w, run
}

func TestALowerShareScalesTheLimitAndMeasuresAgain(t *testing.T) {
	w, run := shareWatcher(t, 0.90, 0.80)
	w.step(context.Background())
	// 8 * 0.80 / 0.90 = 7.1, rounded down.
	if len(run.applied) == 0 || run.applied[0] != 7 {
		t.Fatalf("applied %v, want 7 first: the limit scaled to the lower share", run.applied)
	}
	if run.requests.Load() == 0 {
		t.Error("the benchmark did not run again")
	}
	if !slices.ContainsFunc(run.notes, func(n string) bool { return strings.Contains(n, "0.80") && strings.Contains(n, "0.90") }) {
		t.Errorf("notes %v, want one naming both shares", run.notes)
	}
}

// A new measurement records the share it was measured at.
func TestAMeasurementRecordsItsShare(t *testing.T) {
	w, _ := shareWatcher(t, 0.90, 0.80)
	w.step(context.Background())
	got, ok := load(w.ConfigPath)
	if !ok || got.MemoryShare != 0.80 {
		t.Errorf("saved %+v, want the result measured at share 0.80", got)
	}
}

// Negative: a cached result stands when the share is the same, a little
// lower, exactly 0.05 lower, higher, or not known.
func TestTheCachedResultStandsOtherwise(t *testing.T) {
	for name, tc := range map[string]struct{ measured, now float64 }{
		"same":            {0.81, 0.81},
		"0.03 lower":      {0.81, 0.78},
		"exactly 0.05":    {0.81, 0.76},
		"higher":          {0.70, 0.81},
		"share not known": {0.81, 0},
		"measured before": {0, 0.70},
	} {
		w, run := shareWatcher(t, tc.measured, tc.now)
		w.step(context.Background())
		if !slices.Equal(run.applied, []int{8}) || run.requests.Load() != 0 {
			t.Errorf("%s: applied %v after %d engine requests, want the cached 8 and no new run", name, run.applied, run.requests.Load())
		}
	}
}

// The scaled limit never drops below 1.
func TestTheScaledLimitIsAtLeastOne(t *testing.T) {
	w, run := shareWatcher(t, 0.90, 0.05)
	if err := save(w.ConfigPath, Result{Method: countMethod, Fingerprint: "fp", Concurrency: 1, MemoryShare: 0.90}); err != nil {
		t.Fatal(err)
	}
	w.step(context.Background())
	if len(run.applied) == 0 || run.applied[0] != 1 {
		t.Errorf("applied %v, want 1 first", run.applied)
	}
}
