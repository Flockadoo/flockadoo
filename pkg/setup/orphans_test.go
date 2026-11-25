package setup

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/engine"
)

// Engine start memory plan, item E4. Both places the client starts an engine
// (the start before the dashboard, and the setup wizard) stop orphaned engine
// processes first, so the memory they held is free when the engine loads, and
// say what they stopped. A failed check is reported and the start goes on.

var macminiOrphan = engine.Orphan{PID: 41467, RSSBytes: 6_082_560 * 1024, Age: 7*24*time.Hour + 18*time.Hour}

func recordSweep(order *[]string, stopped []engine.Orphan, err error) func(context.Context) ([]engine.Orphan, error) {
	return func(context.Context) ([]engine.Orphan, error) {
		*order = append(*order, "sweep")
		return stopped, err
	}
}

func useSweep(t *testing.T, sweep func(context.Context) ([]engine.Orphan, error)) {
	t.Helper()
	old := stopOrphans
	t.Cleanup(func() { stopOrphans = old })
	stopOrphans = sweep
}

// A configured Solo machine whose engine is down and starts on its own.
func autoStartConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	cfg.Provider.Engine = "vllm"
	cfg.Provider.Model = "org/m"
	cfg.Provider.LLMURL = stoppedEngine(t)
	cfg.Provider.EngineBin = dummyEngineBin(t)
	cfg.Provider.AutoStart = true
	cfg.Logging.LogDir = t.TempDir()
	return cfg
}

func TestTheStartStopsOrphansBeforeItStartsTheEngine(t *testing.T) {
	var order []string
	errStop := errors.New("test: stopped at the start")
	revision := func(context.Context, *config.Config) error {
		order = append(order, "start")
		return errStop
	}
	var out bytes.Buffer
	err := ensureOnStartWith(t.Context(), autoStartConfig(t), Streams{In: strings.NewReader(""), Out: &out}, false, revision, recordSweep(&order, []engine.Orphan{macminiOrphan}, nil))
	if !errors.Is(err, errStop) {
		t.Fatalf("ensureOnStartWith = %v", err)
	}
	if !slices.Equal(order, []string{"sweep", "start"}) {
		t.Errorf("order %v, want the orphans stopped before the start", order)
	}
	if !strings.Contains(out.String(), "41467") || !strings.Contains(out.String(), "orphaned") {
		t.Errorf("output does not report the stopped orphan:\n%s", out.String())
	}
}

// The report EnsureOnStartAt returns carries what was stopped, for the dashboard.
func TestTheStartReportCarriesTheStoppedOrphans(t *testing.T) {
	var order []string
	useSweep(t, recordSweep(&order, []engine.Orphan{macminiOrphan}, nil))
	cfg := autoStartConfig(t)
	platform := httptest.NewServer(nil)
	platform.Close()
	cfg.Provider.URL = platform.URL

	var out bytes.Buffer
	report, _ := EnsureOnStartAt(t.Context(), cfg, "", Streams{In: strings.NewReader(""), Out: &out, Err: &out})
	if len(report.StoppedOrphans) != 1 || report.StoppedOrphans[0].PID != 41467 {
		t.Errorf("report %+v, want the stopped orphan", report)
	}
	if note := report.Note(); !strings.Contains(note, "41467") || !strings.Contains(note, "5.8 GiB") {
		t.Errorf("note %q, want the orphan's pid and memory", note)
	}
}

// Negative: a start with nothing to clean up shows no Engine cleanup line.
func TestAnEmptyStartReportHasNoNote(t *testing.T) {
	if note := (StartReport{}).Note(); note != "" {
		t.Errorf("note %q, want none", note)
	}
}

func TestTwoStoppedOrphansAreBothInTheNote(t *testing.T) {
	second := engine.Orphan{PID: 7002, RSSBytes: 2 << 30, Age: 3 * time.Hour}
	note := StartReport{StoppedOrphans: []engine.Orphan{macminiOrphan, second}}.Note()
	if !strings.Contains(note, "41467") || !strings.Contains(note, "7002") {
		t.Errorf("note %q, want both pids", note)
	}
}

func TestTheWizardStopsOrphansBeforeItStartsTheEngine(t *testing.T) {
	var order []string
	recordStarts(t, &order)
	useSweep(t, recordSweep(&order, []engine.Orphan{macminiOrphan}, nil))
	var out strings.Builder
	_ = maybeStart(engine.KindVLLM, vllmSpec, startConfig(t), Options{Yes: true}, true, Streams{Out: &out, Err: &out}, nil)
	if !slices.Equal(order, []string{"sweep", "start"}) {
		t.Errorf("order %v, want the orphans stopped before the start", order)
	}
	if !strings.Contains(out.String(), "41467") {
		t.Errorf("output does not report the stopped orphan:\n%s", out.String())
	}
}

// Negative: a failed check (no ps, say) is reported, and the engine still starts.
func TestAFailedOrphanCheckDoesNotStopTheStart(t *testing.T) {
	var order []string
	recordStarts(t, &order)
	useSweep(t, recordSweep(&order, nil, errors.New("ps: executable file not found")))
	var out strings.Builder
	_ = maybeStart(engine.KindVLLM, vllmSpec, startConfig(t), Options{Yes: true}, true, Streams{Out: &out, Err: &out}, nil)
	if !slices.Equal(order, []string{"sweep", "start"}) {
		t.Errorf("order %v, want the start after a failed check", order)
	}
	if !strings.Contains(out.String(), "ps: executable file not found") {
		t.Errorf("output does not report the failed check:\n%s", out.String())
	}
}
