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
// (the start before the dashboard, and the setup wizard) first report vLLM
// engine processes whose API server is gone, with how to stop them; they stop
// nothing. A failed check is reported and the start goes on.

var macminiOrphan = engine.Orphan{PID: 41467, RSSBytes: 6_082_560 * 1024, Age: 7*24*time.Hour + 18*time.Hour}

func recordScan(order *[]string, found []engine.Orphan, err error) func(context.Context) ([]engine.Orphan, error) {
	return func(context.Context) ([]engine.Orphan, error) {
		*order = append(*order, "scan")
		return found, err
	}
}

func useScan(t *testing.T, scan func(context.Context) ([]engine.Orphan, error)) {
	t.Helper()
	old := scanOrphans
	t.Cleanup(func() { scanOrphans = old })
	scanOrphans = scan
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

func TestTheStartReportsOrphansBeforeItStartsTheEngine(t *testing.T) {
	var order []string
	errStop := errors.New("test: stopped at the start")
	revision := func(context.Context, *config.Config) error {
		order = append(order, "start")
		return errStop
	}
	var out bytes.Buffer
	err := ensureOnStartWith(t.Context(), autoStartConfig(t), Streams{In: strings.NewReader(""), Out: &out}, false, revision, recordScan(&order, []engine.Orphan{macminiOrphan}, nil))
	if !errors.Is(err, errStop) {
		t.Fatalf("ensureOnStartWith = %v", err)
	}
	if !slices.Equal(order, []string{"scan", "start"}) {
		t.Errorf("order %v, want the orphans reported before the start", order)
	}
	if !strings.Contains(out.String(), "41467") || !strings.Contains(out.String(), "kill 41467") {
		t.Errorf("output does not report the orphan and how to stop it:\n%s", out.String())
	}
}

// The report EnsureOnStartAt returns carries what was found, for the dashboard.
func TestTheStartReportCarriesTheOrphans(t *testing.T) {
	var order []string
	useScan(t, recordScan(&order, []engine.Orphan{macminiOrphan}, nil))
	cfg := autoStartConfig(t)
	platform := httptest.NewServer(nil)
	platform.Close()
	cfg.Provider.URL = platform.URL

	var out bytes.Buffer
	report, _ := EnsureOnStartAt(t.Context(), cfg, "", Streams{In: strings.NewReader(""), Out: &out, Err: &out})
	if len(report.Orphans) != 1 || report.Orphans[0].PID != 41467 {
		t.Errorf("report %+v, want the orphan", report)
	}
	note := report.Note()
	for _, want := range []string{"41467", "5.8 GiB", "kill 41467"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q does not say %q", note, want)
		}
	}
}

// Negative: a start that found nothing shows no Orphaned engines line.
func TestAnEmptyStartReportHasNoNote(t *testing.T) {
	if note := (StartReport{}).Note(); note != "" {
		t.Errorf("note %q, want none", note)
	}
}

func TestTwoOrphansAreBothInTheNote(t *testing.T) {
	second := engine.Orphan{PID: 7002, RSSBytes: 2 << 30, Age: 3 * time.Hour}
	note := StartReport{Orphans: []engine.Orphan{macminiOrphan, second}}.Note()
	for _, want := range []string{"2 vLLM engine processes", "41467", "7002", "kill 41467 7002"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q does not say %q", note, want)
		}
	}
}

func TestTheWizardReportsOrphansBeforeItStartsTheEngine(t *testing.T) {
	var order []string
	recordStarts(t, &order)
	useScan(t, recordScan(&order, []engine.Orphan{macminiOrphan}, nil))
	var out strings.Builder
	_ = maybeStart(engine.KindVLLM, vllmSpec, startConfig(t), Options{Yes: true}, true, Streams{Out: &out, Err: &out}, nil)
	if !slices.Equal(order, []string{"scan", "start"}) {
		t.Errorf("order %v, want the orphans reported before the start", order)
	}
	if !strings.Contains(out.String(), "kill 41467") {
		t.Errorf("output does not report the orphan:\n%s", out.String())
	}
}

// Negative: a failed check (no ps, say) is reported, and the engine still starts.
func TestAFailedOrphanCheckDoesNotStopTheStart(t *testing.T) {
	var order []string
	recordStarts(t, &order)
	useScan(t, recordScan(&order, nil, errors.New("ps: executable file not found")))
	var out strings.Builder
	_ = maybeStart(engine.KindVLLM, vllmSpec, startConfig(t), Options{Yes: true}, true, Streams{Out: &out, Err: &out}, nil)
	if !slices.Equal(order, []string{"scan", "start"}) {
		t.Errorf("order %v, want the start after a failed check", order)
	}
	if !strings.Contains(out.String(), "ps: executable file not found") {
		t.Errorf("output does not report the failed check:\n%s", out.String())
	}
}
