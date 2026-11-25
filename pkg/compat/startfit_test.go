package compat

import (
	"errors"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// Engine start memory plan, item E3. Before the engine starts, the client
// compares what the model needs (its weights plus one request at its full
// context, the compat estimate) with the memory free for the engine now: free
// VRAM on a dedicated GPU, free system memory on unified memory (a GB10). A
// model that does not fit is refused in plain words, with both numbers,
// instead of a vLLM stack trace. When the client cannot tell, it does not
// refuse.

const gib = int64(1 << 30)

func i64(n int64) *int64 { return &n }

// A 9 GiB model with 128 KiB of KV cache per token: at 32,768 tokens it needs
// 9 * 1.20 + 4 = 14.8 GiB.
var model14 = verify.CatalogEntry{
	Alias:                "Qwen/Qwen2.5-14B-Instruct",
	ServiceType:          "vllm",
	MinSizeBytes:         9 * gib,
	MaxModelLen:          i64(32768),
	KVCacheBytesPerToken: i64(128 << 10),
	IsActive:             true,
}

func dedicatedGPU(free int64) *Hardware {
	return &Hardware{MemoryKind: MemoryVRAM, GPUCount: 1, MemoryTotalBytes: 24 * gib, MemoryFreeBytes: free, UsableBytes: 24 * gib}
}

func TestAModelThatDoesNotFitIsRefusedWithBothNumbers(t *testing.T) {
	err := StartCheck(dedicatedGPU(10*gib), model14, 0)
	var dnf *DoesNotFit
	if !errors.As(err, &dnf) {
		t.Fatalf("StartCheck = %v, want DoesNotFit", err)
	}
	msg := err.Error()
	for _, want := range []string{"Qwen/Qwen2.5-14B-Instruct", "14.80 GiB", "32,768-token context", "10.00 GiB", "GPU memory", "Close other programs", "smaller model", "lower the context"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q does not say %q", msg, want)
		}
	}
}

func TestAModelThatFitsIsNotRefused(t *testing.T) {
	if err := StartCheck(dedicatedGPU(20*gib), model14, 0); err != nil {
		t.Errorf("StartCheck = %v, want nil: 14.8 GiB fits in 20 GiB free", err)
	}
}

// The context the config sets is the one the engine starts with.
func TestTheConfiguredContextIsTheOneChecked(t *testing.T) {
	if err := StartCheck(dedicatedGPU(12*gib), model14, 8192); err != nil {
		t.Errorf("StartCheck at 8,192 tokens = %v, want nil: 10.8 + 1 GiB fits in 12 GiB", err)
	}
	err := StartCheck(dedicatedGPU(12*gib), model14, 65536)
	if err == nil || !strings.Contains(err.Error(), "65,536-token context") {
		t.Errorf("StartCheck at 65,536 tokens = %v, want a refusal naming that context", err)
	}
}

// On unified memory (a GB10) the free memory is the system's, shared with the CPU.
func TestUnifiedMemoryIsCheckedAgainstFreeSystemMemory(t *testing.T) {
	hw := &Hardware{OS: "linux", SystemRAMBytes: gb10Total}
	if err := nvidiaHardware(hw, gb10CSV, "13.0", gb10Mem); err != nil {
		t.Fatal(err)
	}
	big := verify.CatalogEntry{Alias: "big", ServiceType: "vllm", MinSizeBytes: 90 * gib, MaxModelLen: i64(32768), KVCacheBytesPerToken: i64(128 << 10), IsActive: true}
	err := StartCheck(hw, big, 0) // 112 GiB needed, 105.25 GiB free
	if err == nil || !strings.Contains(err.Error(), "shared by the CPU and GPU") {
		t.Errorf("StartCheck = %v, want a refusal about memory shared by the CPU and GPU", err)
	}
	if err := StartCheck(hw, model14, 0); err != nil {
		t.Errorf("StartCheck = %v, want nil for a 14.8 GiB model with 105 GiB free", err)
	}
}

// Negative: when the client cannot tell, it does not refuse.
func TestWithoutTheNumbersNothingIsRefused(t *testing.T) {
	cases := map[string]struct {
		hw    *Hardware
		entry verify.CatalogEntry
	}{
		"no hardware":           {nil, model14},
		"free memory unknown":   {&Hardware{MemoryKind: MemoryUnified, UnifiedMemory: true, GPUCount: 1, MemoryTotalBytes: 16 * gib, UsableBytes: 10 * gib}, model14},
		"no GPU":                {&Hardware{MemoryKind: MemorySystem, MemoryTotalBytes: 64 * gib, MemoryFreeBytes: 1 * gib, UsableBytes: 40 * gib}, model14},
		"several GPUs":          {&Hardware{MemoryKind: MemoryVRAM, GPUCount: 4, MemoryTotalBytes: 24 * gib, MemoryFreeBytes: 1 * gib, UsableBytes: 24 * gib}, model14},
		"model size unknown":    {dedicatedGPU(1 * gib), verify.CatalogEntry{Alias: "x", ServiceType: "vllm", IsActive: true}},
		"enough with no margin": {dedicatedGPU(15 * gib), model14},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := StartCheck(tc.hw, tc.entry, 0); err != nil {
				t.Errorf("StartCheck = %v, want nil", err)
			}
		})
	}
}

// E2: on unified memory vLLM starts with a share of the memory (free less a
// margin), so that share, not all the free memory, is what the model must fit.
// peipei: 0.81 of 121.86 GiB is 98.7 GiB, with 105.25 GiB free. A model that
// needs 100 GiB fits the free memory but not the share.
func TestOnUnifiedMemoryVLLMMustFitItsShare(t *testing.T) {
	hw := &Hardware{OS: "linux", SystemRAMBytes: gb10Total}
	if err := nvidiaHardware(hw, gb10CSV, "13.0", gb10Mem); err != nil {
		t.Fatal(err)
	}
	// 80 GiB * 1.20 + 4 GiB of KV cache = 100 GiB.
	needs100 := verify.CatalogEntry{Alias: "big", ServiceType: "vllm", MinSizeBytes: 80 * gib, MaxModelLen: i64(32768), KVCacheBytesPerToken: i64(128 << 10), IsActive: true}
	err := StartCheck(hw, needs100, 0)
	var dnf *DoesNotFit
	if !errors.As(err, &dnf) {
		t.Fatalf("StartCheck = %v, want DoesNotFit: 100 GiB does not fit vLLM's 0.81 share", err)
	}
	for _, want := range []string{"0.81", "121.86 GiB", "105.25 GiB"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
}

// Negative: llama.cpp takes no memory share; on the same machine the same
// need is checked against all the free memory.
func TestLlamaCppOnUnifiedMemoryUsesAllTheFreeMemory(t *testing.T) {
	hw := &Hardware{OS: "linux", SystemRAMBytes: gb10Total}
	if err := nvidiaHardware(hw, gb10CSV, "13.0", gb10Mem); err != nil {
		t.Fatal(err)
	}
	// 80 GiB * 1.20 + 4 GiB of KV cache = 100 GiB, under 105.25 GiB free.
	gguf := verify.CatalogEntry{Alias: "big-gguf", ServiceType: "llama_cpp", MinSizeBytes: 80 * gib, MaxModelLen: i64(32768), KVCacheBytesPerToken: i64(128 << 10), IsActive: true}
	if err := StartCheck(hw, gguf, 0); err != nil {
		t.Errorf("StartCheck = %v, want nil", err)
	}
}
