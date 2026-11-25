package setup

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/compat"
	"github.com/Flockadoo/flockadoo/pkg/engine"
)

// Engine start memory plan, item E2. On unified memory vLLM (CUDA) starts with
// a memory share that fits the memory free at that moment, read right before
// each start (decision D1). Dedicated GPUs, vLLM Metal (until R shows it
// honours the flag), llama.cpp and flocks get no share, as before. A share
// the operator wrote into the engine command is kept and reported.

// peipei, 2026-11-24: 121.86 GiB total, 105.25 GiB free.
const (
	peipeiTotal = int64(127779096) * 1024
	peipeiFree  = int64(110362624) * 1024
)

func gb10() *compat.Hardware {
	return &compat.Hardware{ProductName: "NVIDIA GB10", GPUCount: 1, MemoryKind: compat.MemoryUnified, UnifiedMemory: true, MemoryTotalBytes: peipeiTotal, MemoryFreeBytes: peipeiFree}
}

func soloVLLM() *config.Config {
	cfg := &config.Config{}
	cfg.Provider.Engine = "vllm"
	cfg.Provider.Model = "org/m"
	return cfg
}

var serveArgs = []string{"serve", "org/m", "--host", "127.0.0.1", "--port", "8000"}

func TestVLLMOnAGB10GetsAShareThatFits(t *testing.T) {
	got := shareChoiceFor(soloVLLM(), engine.KindVLLM, serveArgs, gb10())
	want := ShareChoice{Share: 0.81, TotalBytes: peipeiTotal, FreeBytes: peipeiFree}
	if got != want {
		t.Errorf("shareChoiceFor = %+v, want %+v", got, want)
	}
	if note := got.Note(); note != "0.81 of 121.86 GiB, chosen at start (105.25 GiB was free)" {
		t.Errorf("Note = %q", note)
	}
}

// A share the operator wrote into the saved command is theirs: it is kept,
// and reported as set in the command.
func TestTheOperatorsOwnShareIsKept(t *testing.T) {
	args := append(slices.Clone(serveArgs), "--gpu-memory-utilization", "0.7")
	got := shareChoiceFor(soloVLLM(), engine.KindVLLM, args, gb10())
	if got.Share != 0.7 || !got.FromCommand {
		t.Errorf("shareChoiceFor = %+v, want 0.7 from the command", got)
	}
	if note := got.Note(); note != "0.70, set in the engine command" {
		t.Errorf("Note = %q", note)
	}
	if spec := withShare(engine.Spec{Args: args}, got); !slices.Equal(spec.Args, args) {
		t.Errorf("withShare changed the operator's command to %v", spec.Args)
	}
}

// Negative: everything that is not vLLM (CUDA) on unified memory on a Solo
// machine starts as before, with no share.
func TestOtherStartsGetNoShare(t *testing.T) {
	dedicated := &compat.Hardware{ProductName: "NVIDIA GeForce RTX 4090", GPUCount: 1, MemoryKind: compat.MemoryVRAM, MemoryTotalBytes: 24 << 30, MemoryFreeBytes: 20 << 30}
	apple := &compat.Hardware{ProductName: "Apple M4", GPUCount: 1, MemoryKind: compat.MemoryUnified, UnifiedMemory: true, MemoryTotalBytes: 16 << 30, MemoryFreeBytes: 12 << 30}
	master := soloVLLM()
	master.Provider.FlockRole = "master"
	for name, tc := range map[string]struct {
		cfg  *config.Config
		kind engine.Kind
		hw   *compat.Hardware
	}{
		"dedicated GPU":     {soloVLLM(), engine.KindVLLM, dedicated},
		"vLLM Metal":        {soloVLLM(), engine.KindVLLMMetal, apple},
		"llama.cpp":         {soloVLLM(), engine.KindLlamaCpp, gb10()},
		"flock master":      {master, engine.KindVLLM, gb10()},
		"no hardware":       {soloVLLM(), engine.KindVLLM, nil},
		"free not readable": {soloVLLM(), engine.KindVLLM, &compat.Hardware{GPUCount: 1, MemoryKind: compat.MemoryUnified, UnifiedMemory: true, MemoryTotalBytes: peipeiTotal}},
	} {
		got := shareChoiceFor(tc.cfg, tc.kind, serveArgs, tc.hw)
		if got != (ShareChoice{}) || got.Note() != "" {
			t.Errorf("%s: shareChoiceFor = %+v (%q), want none", name, got, got.Note())
		}
		if spec := withShare(engine.Spec{Args: serveArgs}, got); !slices.Equal(spec.Args, serveArgs) {
			t.Errorf("%s: withShare = %v, want the command unchanged", name, spec.Args)
		}
	}
}

func TestAChosenShareIsPassedToVLLM(t *testing.T) {
	spec := withShare(engine.Spec{Args: serveArgs}, shareChoiceFor(soloVLLM(), engine.KindVLLM, serveArgs, gb10()))
	if !strings.Contains(strings.Join(spec.Args, " "), "--gpu-memory-utilization 0.81") {
		t.Errorf("args %v, want --gpu-memory-utilization 0.81", spec.Args)
	}
}

func useShare(t *testing.T, choice ShareChoice) {
	t.Helper()
	old := chooseShare
	t.Cleanup(func() { chooseShare = old })
	chooseShare = func(context.Context, *config.Config, engine.Kind, []string) ShareChoice { return choice }
}

func captureStart(t *testing.T, started *engine.Spec) {
	t.Helper()
	old := startDetached
	t.Cleanup(func() { startDetached = old })
	startDetached = func(spec engine.Spec, _ string) (<-chan error, error) {
		*started = spec
		return nil, errors.New("test: stopped at the start")
	}
}

func TestTheWizardStartsVLLMWithTheChosenShare(t *testing.T) {
	useShare(t, ShareChoice{Share: 0.81, TotalBytes: peipeiTotal, FreeBytes: peipeiFree})
	var started engine.Spec
	captureStart(t, &started)
	var out strings.Builder
	_ = maybeStart(engine.KindVLLM, vllmSpec, startConfig(t), Options{Yes: true}, true, Streams{Out: &out, Err: &out}, nil)
	if !strings.Contains(strings.Join(started.Args, " "), "--gpu-memory-utilization 0.81") {
		t.Errorf("started %v, want --gpu-memory-utilization 0.81", started.Args)
	}
	if !strings.Contains(out.String(), "0.81 of 121.86 GiB") {
		t.Errorf("output does not say the share:\n%s", out.String())
	}
}

// Negative: with no share chosen the wizard starts the command as built.
func TestTheWizardWithoutAShareStartsTheCommandAsBuilt(t *testing.T) {
	useShare(t, ShareChoice{})
	var started engine.Spec
	captureStart(t, &started)
	var out strings.Builder
	_ = maybeStart(engine.KindVLLM, vllmSpec, startConfig(t), Options{Yes: true}, true, Streams{Out: &out, Err: &out}, nil)
	if !slices.Equal(started.Args, vllmSpec.Args) {
		t.Errorf("started %v, want %v", started.Args, vllmSpec.Args)
	}
}
