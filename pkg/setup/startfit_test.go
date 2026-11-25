package setup

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/compat"
	"github.com/Flockadoo/flockadoo/pkg/engine"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// Engine start memory plan, item E3. Both places the client starts an engine
// check, after reporting orphans and before any download or start, that the
// model fits the memory free now; a model that does not fit is not started,
// and the message names any orphan holding memory. When the client cannot
// tell (no catalog, a flock, no free-memory reading), it starts as before.

const fitGiB = int64(1 << 30)

func fitI64(n int64) *int64 { return &n }

// 9 GiB of weights and 128 KiB of KV cache per token: 14.8 GiB at 32,768 tokens.
var fitEntry = verify.CatalogEntry{
	Alias:                "org/m",
	ServiceType:          "vllm",
	MinSizeBytes:         9 * fitGiB,
	MaxModelLen:          fitI64(32768),
	KVCacheBytesPerToken: fitI64(128 << 10),
	IsActive:             true,
}

func gpuWithFree(free int64) *compat.Hardware {
	return &compat.Hardware{MemoryKind: compat.MemoryVRAM, GPUCount: 1, MemoryTotalBytes: 24 * fitGiB, MemoryFreeBytes: free, UsableBytes: 24 * fitGiB}
}

func fitDeps(hw *compat.Hardware, catalogCalls *int) startFitDeps {
	return startFitDeps{
		catalog: func(context.Context, *config.Config) (verify.CatalogEntry, bool, error) {
			*catalogCalls++
			return fitEntry, true, nil
		},
		detect: func(context.Context) (*compat.Hardware, error) { return hw, nil },
	}
}

func fitConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Provider.Engine = "vllm"
	cfg.Provider.Model = "org/m"
	return cfg
}

func TestAModelThatDoesNotFitIsNotStarted(t *testing.T) {
	var calls int
	err := startFitWith(t.Context(), fitConfig(), nil, fitDeps(gpuWithFree(10*fitGiB), &calls))
	var dnf *compat.DoesNotFit
	if !errors.As(err, &dnf) {
		t.Fatalf("startFitWith = %v, want DoesNotFit", err)
	}
	if !strings.Contains(err.Error(), "14.80 GiB") || !strings.Contains(err.Error(), "10.00 GiB") {
		t.Errorf("%q does not give both numbers", err)
	}
}

// The refusal names the orphans holding memory, and how to stop them.
func TestTheRefusalNamesOrphansHoldingMemory(t *testing.T) {
	var calls int
	err := startFitWith(t.Context(), fitConfig(), []engine.Orphan{macminiOrphan}, fitDeps(gpuWithFree(10*fitGiB), &calls))
	if err == nil {
		t.Fatal("startFitWith = nil, want a refusal")
	}
	for _, want := range []string{"41467", "5.8 GiB", "kill 41467"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
}

func TestAModelThatFitsStarts(t *testing.T) {
	var calls int
	if err := startFitWith(t.Context(), fitConfig(), nil, fitDeps(gpuWithFree(20*fitGiB), &calls)); err != nil {
		t.Errorf("startFitWith = %v, want nil", err)
	}
}

// The context the config sets is the one the engine starts with.
func TestTheConfiguredContextIsChecked(t *testing.T) {
	var calls int
	cfg := fitConfig()
	cfg.Provider.MaxModelLen = 65536
	err := startFitWith(t.Context(), cfg, nil, fitDeps(gpuWithFree(16*fitGiB), &calls))
	if err == nil || !strings.Contains(err.Error(), "65,536-token context") {
		t.Errorf("startFitWith = %v, want a refusal at the configured 65,536 tokens", err)
	}
}

// Negative: a flock's memory is planned across machines; one machine's free
// memory says nothing, so a flock member is never refused or even checked.
func TestAFlockMemberIsNotChecked(t *testing.T) {
	for _, role := range []string{"master", "worker"} {
		var calls int
		cfg := fitConfig()
		cfg.Provider.FlockRole = role
		if err := startFitWith(t.Context(), cfg, nil, fitDeps(gpuWithFree(1*fitGiB), &calls)); err != nil || calls != 0 {
			t.Errorf("%s: startFitWith = %v with %d catalog reads, want nil and none", role, err, calls)
		}
	}
}

// Negative: without the catalog entry the client cannot tell, and starts.
func TestWithoutTheCatalogTheStartGoesOn(t *testing.T) {
	for name, catalog := range map[string]func(context.Context, *config.Config) (verify.CatalogEntry, bool, error){
		"unreachable": func(context.Context, *config.Config) (verify.CatalogEntry, bool, error) {
			return verify.CatalogEntry{}, false, errors.New("connection refused")
		},
		"not listed": func(context.Context, *config.Config) (verify.CatalogEntry, bool, error) {
			return verify.CatalogEntry{}, false, nil
		},
	} {
		d := startFitDeps{catalog: catalog, detect: func(context.Context) (*compat.Hardware, error) { return gpuWithFree(1 * fitGiB), nil }}
		if err := startFitWith(t.Context(), fitConfig(), nil, d); err != nil {
			t.Errorf("%s: startFitWith = %v, want nil", name, err)
		}
	}
}

func useFit(t *testing.T, order *[]string, err error) {
	t.Helper()
	old := checkStartFit
	t.Cleanup(func() { checkStartFit = old })
	checkStartFit = func(context.Context, *config.Config, []engine.Orphan) error {
		*order = append(*order, "fit")
		return err
	}
}

var errDoesNotFit = &compat.DoesNotFit{Model: "org/m", Need: 15 * fitGiB, Free: 10 * fitGiB, Kind: compat.MemoryVRAM}

func TestTheStartChecksTheFitBeforeItStarts(t *testing.T) {
	var order []string
	useFit(t, &order, errDoesNotFit)
	revision := func(context.Context, *config.Config) error {
		order = append(order, "start")
		return nil
	}
	var out bytes.Buffer
	err := ensureOnStartWith(t.Context(), autoStartConfig(t), Streams{In: strings.NewReader(""), Out: &out}, false, revision, recordScan(&order, nil, nil))
	if !errors.Is(err, errDoesNotFit) {
		t.Errorf("ensureOnStartWith = %v, want the refusal", err)
	}
	if !slices.Equal(order, []string{"scan", "fit"}) {
		t.Errorf("order %v, want the fit checked after the orphan report and nothing started", order)
	}
}

func TestTheWizardChecksTheFitBeforeItDownloadsOrStarts(t *testing.T) {
	var order []string
	recordStarts(t, &order)
	useScan(t, recordScan(&order, nil, nil))
	useFit(t, &order, errDoesNotFit)
	download := func(context.Context) error { order = append(order, "download"); return nil }
	var out strings.Builder
	err := maybeStart(engine.KindVLLM, vllmSpec, startConfig(t), Options{Yes: true}, true, Streams{Out: &out, Err: &out}, download)
	if !errors.Is(err, errDoesNotFit) {
		t.Errorf("maybeStart = %v, want the refusal", err)
	}
	if !slices.Equal(order, []string{"scan", "fit"}) {
		t.Errorf("order %v, want no download and no start after the refusal", order)
	}
}

// Negative: a model that fits goes on to the download and the start.
func TestTheWizardStartsAModelThatFits(t *testing.T) {
	var order []string
	recordStarts(t, &order)
	useScan(t, recordScan(&order, nil, nil))
	useFit(t, &order, nil)
	download := func(context.Context) error { order = append(order, "download"); return nil }
	var out strings.Builder
	_ = maybeStart(engine.KindVLLM, vllmSpec, startConfig(t), Options{Yes: true}, true, Streams{Out: &out, Err: &out}, download)
	if !slices.Equal(order, []string{"scan", "fit", "download", "start"}) {
		t.Errorf("order %v, want scan, fit, download, start", order)
	}
}
