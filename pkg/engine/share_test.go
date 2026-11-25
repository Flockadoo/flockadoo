package engine

import (
	"slices"
	"testing"
)

// Engine start memory plan, item E2. vLLM reserves --gpu-memory-utilization
// of the GPU's total memory at start, 0.92 by default, and refuses when that
// much is not free. On unified memory (a GB10) the CPU, the desktop and every
// other program share the pool: peipei had 105.25 of 121.86 GiB free and vLLM
// refused even bge-m3. On unified memory the client starts vLLM with the share
// that fits the memory free at that moment, leaving a margin:
//
//	share = min(0.92, (free - margin) / total), rounded down to 0.01
//	margin = 4 GiB or 5% of total, whichever is larger
//
// (The plan's example says 0.82 for 105 of 122 GiB free; the formula gives
// 0.81: (105 - 6.1) / 122 = 0.8107.)

const shareGiB = int64(1 << 30)

// peipei, 2026-11-24: 121.86 GiB total, 105.25 GiB free (as /proc/meminfo
// gave them, in kB).
const (
	peipeiTotal = int64(127779096) * 1024
	peipeiFree  = int64(110362624) * 1024
)

func TestTheShareFitsTheMemoryFreeAtStart(t *testing.T) {
	if got := MemoryShare(peipeiTotal, peipeiFree); got != 0.81 {
		t.Errorf("MemoryShare(peipei) = %v, want 0.81", got)
	}
}

func TestTheMarginIsFourGiBOrFivePercent(t *testing.T) {
	if got := ShareMargin(16 * shareGiB); got != 4*shareGiB {
		t.Errorf("ShareMargin(16 GiB) = %d, want 4 GiB", got)
	}
	if got, want := ShareMargin(peipeiTotal), peipeiTotal/20; got != want {
		t.Errorf("ShareMargin(121.86 GiB) = %d, want 5%% (%d)", got, want)
	}
}

func TestTheShareCases(t *testing.T) {
	for name, tc := range map[string]struct {
		total, free int64
		want        float64
	}{
		// 16 GiB with 12 free: (12 - 4) / 16.
		"small machine": {16 * shareGiB, 12 * shareGiB, 0.5},
		// (100 - 5) / 100 = 0.95, capped at vLLM's default.
		"mostly free": {100 * shareGiB, 100 * shareGiB, 0.92},
		// (50 - 5) / 100 = 0.45 exactly.
		"exact hundredth": {100 * shareGiB, 50 * shareGiB, 0.45},
		// 49.9 GiB free: (49.9 - 5) / 100 = 0.449, rounded down.
		"rounded down": {100 * shareGiB, 49*shareGiB + 9*shareGiB/10, 0.44},
		// Less free than the margin: no share fits.
		"nothing fits": {16 * shareGiB, 3 * shareGiB, 0},
		"no reading":   {0, 0, 0},
	} {
		if got := MemoryShare(tc.total, tc.free); got != tc.want {
			t.Errorf("%s: MemoryShare = %v, want %v", name, got, tc.want)
		}
	}
}

func TestAShareIsPassedToVLLM(t *testing.T) {
	spec := Spec{Bin: "vllm", Args: []string{"serve", "org/m", "--port", "8000"}}
	got := spec.WithMemoryShare(0.81)
	if v := flagValues(got.Args, "--gpu-memory-utilization"); !slices.Equal(v, []string{"0.81"}) {
		t.Errorf("args %v, want --gpu-memory-utilization 0.81", got.Args)
	}
	if len(spec.Args) != 4 {
		t.Errorf("WithMemoryShare changed the spec it was given: %v", spec.Args)
	}
}

// Negative: a share the operator wrote into the command stays; vLLM's default
// and no share at all leave the command as it is.
func TestTheCommandIsLeftAloneWhenItShould(t *testing.T) {
	own := Spec{Args: []string{"serve", "org/m", "--gpu-memory-utilization", "0.7"}}
	if got := own.WithMemoryShare(0.81); !slices.Equal(got.Args, own.Args) {
		t.Errorf("args %v, want the operator's 0.7 kept", got.Args)
	}
	plain := Spec{Args: []string{"serve", "org/m"}}
	for _, share := range []float64{0, 0.92, 0.95} {
		if got := plain.WithMemoryShare(share); !slices.Equal(got.Args, plain.Args) {
			t.Errorf("WithMemoryShare(%v) = %v, want no flag", share, got.Args)
		}
	}
}

func TestTheCommandsOwnShareIsRead(t *testing.T) {
	for _, args := range [][]string{
		{"serve", "org/m", "--gpu-memory-utilization", "0.7"},
		{"serve", "org/m", "--gpu-memory-utilization=0.7"},
	} {
		if got, ok := MemoryShareOf(args); !ok || got != 0.7 {
			t.Errorf("MemoryShareOf(%v) = %v, %v; want 0.7", args, got, ok)
		}
	}
	if _, ok := MemoryShareOf([]string{"serve", "org/m"}); ok {
		t.Error("MemoryShareOf found a share in a command without one")
	}
}
