package compat

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// Engine start memory plan, Macs (decided by Charles on 2026-11-25 after R).
// vLLM Metal sizes its KV cache to the memory free at start itself: it reads
// psutil's available memory and shrinks the cache to fit. So a Mac gets no
// memory share from the client (a share that leaves 4 GiB would refuse models
// that start today on a 16 GB Mac). It does get the fit check: the client
// reads free memory the way vLLM Metal does, and refuses a model whose need is
// larger than that before vLLM starts.
//
// testdata/vm_stat_macmini.txt is /usr/bin/vm_stat's output on the macmini
// (M4, 16 GiB, bge-m3 running), copied by bytes on 2026-11-25 (SHA-256
// db376e11...ffd3). psutil's available memory on macOS is free + inactive +
// speculative pages; on the macmini it matched vm_stat's to within a page.

// (40091 free + 295086 inactive + 94913 speculative) pages of 16384 bytes.
const macminiFree = int64(430090) * 16384

func macminiVMStat(t *testing.T) []byte {
	t.Helper()
	out, err := os.ReadFile("testdata/vm_stat_macmini.txt")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAMacsFreeMemoryIsReadTheWayVLLMMetalReadsIt(t *testing.T) {
	got, err := macFreeMemory(string(macminiVMStat(t)))
	if err != nil || got != macminiFree {
		t.Errorf("macFreeMemory = %d, %v; want %d (free + inactive + speculative pages)", got, err, macminiFree)
	}
}

// Negative: output the client cannot read gives no reading, never a guess.
func TestUnreadableVMStatGivesNoReading(t *testing.T) {
	full := string(macminiVMStat(t))
	for name, out := range map[string]string{
		"empty":           "",
		"no page size":    strings.Replace(full, "(page size of 16384 bytes)", "", 1),
		"no inactive":     strings.Replace(full, "Pages inactive:", "Pages idle:", 1),
		"no speculative":  strings.Replace(full, "Pages speculative:", "Pages guessed:", 1),
		"no free counter": strings.Replace(full, "Pages free:", "Pages loose:", 1),
	} {
		if got, err := macFreeMemory(out); err == nil {
			t.Errorf("%s: macFreeMemory = %d, want an error", name, got)
		}
	}
}

func appleM4() *Hardware {
	total := 16 * gib
	return &Hardware{OS: "darwin", Arch: "arm64", ProductName: "Apple M4 (10 cores)", GPUCount: 1,
		MemoryKind: MemoryUnified, UnifiedMemory: true, SystemRAMBytes: total, MemoryTotalBytes: total,
		UsableBytes: int64(float64(total) * appleSiliconUsableFraction)}
}

func TestDetectOnAMacSetsFreeAndUsedMemory(t *testing.T) {
	hw := appleM4()
	setMacFreeMemory(hw, macminiVMStat(t), nil)
	if hw.MemoryFreeBytes != macminiFree || hw.MemoryUsedBytes != 16*gib-macminiFree {
		t.Errorf("free %d used %d, want %d and %d", hw.MemoryFreeBytes, hw.MemoryUsedBytes, macminiFree, 16*gib-macminiFree)
	}
}

// Negative: when vm_stat cannot run, the Mac has no free reading (the fit
// check is then skipped, as before) and says why.
func TestAMacWithoutVMStatHasNoFreeReading(t *testing.T) {
	hw := appleM4()
	setMacFreeMemory(hw, nil, errors.New("exec: vm_stat: not found"))
	if hw.MemoryFreeBytes != 0 || hw.MemoryUsedBytes != 0 {
		t.Errorf("free %d used %d, want no reading", hw.MemoryFreeBytes, hw.MemoryUsedBytes)
	}
	if len(hw.Warnings) == 0 || !strings.Contains(hw.Warnings[len(hw.Warnings)-1], "vm_stat") {
		t.Errorf("warnings %v, want one naming vm_stat", hw.Warnings)
	}
}

// A 3.5 GiB model with 36 KiB of KV cache per token (Qwen2.5-3B's layout): at
// 16,384 tokens it needs 3.5 * 1.20 + 0.5625 = 4.76 GiB.
var model3 = verify.CatalogEntry{
	Alias:                "Qwen/Qwen2.5-3B-Instruct",
	ServiceType:          "vllm",
	MinSizeBytes:         7 * gib / 2,
	MaxModelLen:          i64(16384),
	KVCacheBytesPerToken: i64(36 << 10),
	IsActive:             true,
}

// A model that fits the memory free on a Mac starts. With the GB10's share
// (keep 4 GiB free) it would be refused: (6.56 - 4) GiB is less than 4.76.
func TestAModelThatFitsAMacsFreeMemoryStarts(t *testing.T) {
	hw := appleM4()
	hw.MemoryFreeBytes = macminiFree
	if err := StartCheck(hw, model3, 0); err != nil {
		t.Errorf("StartCheck = %v, want nil: vLLM Metal gets no share and sizes itself to the free memory", err)
	}
}

func TestAModelThatDoesNotFitAMacsFreeMemoryIsRefused(t *testing.T) {
	hw := appleM4()
	hw.MemoryFreeBytes = macminiFree
	err := StartCheck(hw, model14, 0)
	var dnf *DoesNotFit
	if !errors.As(err, &dnf) {
		t.Fatalf("StartCheck = %v, want DoesNotFit", err)
	}
	if dnf.Share != 0 {
		t.Errorf("DoesNotFit share %v, want none: a Mac gets no share", dnf.Share)
	}
	msg := err.Error()
	for _, want := range []string{"14.80 GiB", "6.56 GiB", "shared by the CPU and GPU", "Close other programs", "smaller model"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q does not say %q", msg, want)
		}
	}
	if strings.Contains(msg, "share of") {
		t.Errorf("%q names a share, which a Mac does not get", msg)
	}
}
