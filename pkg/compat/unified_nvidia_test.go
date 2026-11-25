package compat

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// Engine start memory plan, item E1. An NVIDIA GB10 (DGX Spark) shares one
// memory pool between CPU and GPU, and nvidia-smi reports its memory as
// [N/A]: on peipei (2026-11-24) the query answered "NVIDIA GB10, [N/A],
// [N/A]". The client parsed that as 0 bytes of VRAM, so the machine scored as
// "usable memory unknown" (or, after Detect's fallback, as a CPU-only machine).
// It is unified memory, like Apple Silicon: total and free come from the
// system (MemTotal, MemAvailable), and a share of the total is usable.

// The memory fields are as peipei's nvidia-smi answered; the driver version
// and uuid are placeholders.
const gb10CSV = "NVIDIA GB10, 580.95.05, [N/A], [N/A], [N/A], GPU-00000000-0000-0000-0000-000000000000\n"

// peipei's memory on 2026-11-24: 121.86 GiB total, 105.25 GiB free.
const gb10MemInfo = `MemTotal:       127779096 kB
MemFree:        98765432 kB
MemAvailable:   110362624 kB
Buffers:          123456 kB
Cached:         10234567 kB
`

const (
	gb10Total     = int64(127779096) * 1024
	gb10Available = int64(110362624) * 1024
)

func gb10Mem() (memInfo, error) { return parseMemInfo(strings.NewReader(gb10MemInfo)) }

func TestMemInfoGivesTotalAndAvailable(t *testing.T) {
	got, err := parseMemInfo(strings.NewReader(gb10MemInfo))
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != gb10Total || got.Available != gb10Available {
		t.Errorf("parseMemInfo = %+v, want total %d, available %d", got, gb10Total, gb10Available)
	}
}

// Negative: a meminfo without MemAvailable is an error, never 0 bytes free.
func TestMemInfoWithoutMemAvailableIsAnError(t *testing.T) {
	if got, err := parseMemInfo(strings.NewReader("MemTotal:       127779096 kB\nMemFree: 1 kB\n")); err == nil {
		t.Errorf("parseMemInfo = %+v, want an error", got)
	}
}

func TestAGB10IsUnifiedMemoryMeasuredFromTheSystem(t *testing.T) {
	hw := &Hardware{OS: "linux", Arch: "arm64", SystemRAMBytes: gb10Total}
	if err := nvidiaHardware(hw, gb10CSV, "13.0", gb10Mem); err != nil {
		t.Fatal(err)
	}
	if hw.MemoryKind != MemoryUnified || !hw.UnifiedMemory {
		t.Errorf("memory kind %s, unified %v; want unified", hw.MemoryKind, hw.UnifiedMemory)
	}
	if hw.MemoryTotalBytes != gb10Total || hw.MemoryFreeBytes != gb10Available || hw.MemoryUsedBytes != gb10Total-gb10Available {
		t.Errorf("total %d free %d used %d; want %d, %d, %d from meminfo",
			hw.MemoryTotalBytes, hw.MemoryFreeBytes, hw.MemoryUsedBytes, gb10Total, gb10Available, gb10Total-gb10Available)
	}
	share := nvidiaUnifiedUsableFraction
	if want := int64(float64(gb10Total) * share); hw.UsableBytes != want {
		t.Errorf("usable %d, want %d", hw.UsableBytes, want)
	}
	// The GPU's own details still come from nvidia-smi.
	if hw.ProductName != "NVIDIA GB10" || hw.DriverVersion != "580.95.05" || hw.CUDAVersion != "13.0" || hw.GPUCount != 1 {
		t.Errorf("product %q driver %q cuda %q count %d", hw.ProductName, hw.DriverVersion, hw.CUDAVersion, hw.GPUCount)
	}
}

// nvidia-smi also answers [Not Supported] for a field it cannot read.
func TestNotSupportedMemoryIsUnifiedToo(t *testing.T) {
	hw := &Hardware{OS: "linux", SystemRAMBytes: gb10Total}
	csv := "NVIDIA GB10, 580.95.05, [Not Supported], [Not Supported], [Not Supported], GPU-0\n"
	if err := nvidiaHardware(hw, csv, "13.0", gb10Mem); err != nil {
		t.Fatal(err)
	}
	if hw.MemoryKind != MemoryUnified || hw.MemoryTotalBytes != gb10Total {
		t.Errorf("memory kind %s total %d; want unified with %d", hw.MemoryKind, hw.MemoryTotalBytes, gb10Total)
	}
}

// Negative: a GPU that reports its memory keeps scoring against its VRAM, and
// the system's memory is not read.
func TestADedicatedGPUIsUnchanged(t *testing.T) {
	hw := &Hardware{OS: "linux", SystemRAMBytes: 64 << 30}
	csv := `"NVIDIA GeForce RTX 4090", 550.54.15, 24564, 1024, 23540, GPU-1` + "\n"
	mem := func() (memInfo, error) {
		t.Error("system memory read for a GPU that reports its own")
		return memInfo{}, errors.New("unused")
	}
	if err := nvidiaHardware(hw, csv, "12.4", mem); err != nil {
		t.Fatal(err)
	}
	const mib = 1024 * 1024
	if hw.MemoryKind != MemoryVRAM || hw.UnifiedMemory {
		t.Errorf("memory kind %s, unified %v; want vram", hw.MemoryKind, hw.UnifiedMemory)
	}
	if hw.MemoryTotalBytes != 24564*mib || hw.MemoryFreeBytes != 23540*mib || hw.UsableBytes != 24564*mib {
		t.Errorf("total %d free %d usable %d", hw.MemoryTotalBytes, hw.MemoryFreeBytes, hw.UsableBytes)
	}
}

// Negative: memory nvidia-smi does not report and the system cannot give is an
// error, so Detect falls back with a warning instead of scoring on 0 bytes.
func TestUnreportedMemoryWithoutMeminfoIsAnError(t *testing.T) {
	hw := &Hardware{OS: "linux"}
	mem := func() (memInfo, error) { return memInfo{}, errors.New("no /proc/meminfo") }
	if err := nvidiaHardware(hw, gb10CSV, "13.0", mem); err == nil {
		t.Errorf("nvidiaHardware = nil, hw %+v; want an error", hw)
	}
}

func TestBGEM3RunsWellOnAGB10(t *testing.T) {
	hw := &Hardware{OS: "linux", Arch: "arm64", SystemRAMBytes: gb10Total}
	if err := nvidiaHardware(hw, gb10CSV, "13.0", gb10Mem); err != nil {
		t.Fatal(err)
	}
	maxLen := int64(8192)
	got := ScoreModel(hw, verify.CatalogEntry{
		Alias:        "baai/bge-m3",
		ServiceType:  "vllm",
		MinSizeBytes: 2_271_064_456,
		MaxModelLen:  &maxLen,
		IsActive:     true,
	})
	if got.Status != StatusRunsWell {
		t.Errorf("status %s (%s), want runs_well", got.Status, got.Reason)
	}
	if !strings.Contains(got.Reason, "unified") || strings.Contains(got.Reason, "CPU") {
		t.Errorf("reason %q, want unified memory and no CPU path", got.Reason)
	}
}

// A tight model on a GB10 is not described as Apple Silicon.
func TestATightModelOnAGB10IsNotCalledAppleSilicon(t *testing.T) {
	hw := &Hardware{OS: "linux", Arch: "arm64", SystemRAMBytes: gb10Total}
	if err := nvidiaHardware(hw, gb10CSV, "13.0", gb10Mem); err != nil {
		t.Fatal(err)
	}
	// 60 GiB * 1.50 = 90 GiB of about 97.5 GiB usable.
	got := ScoreModel(hw, verify.CatalogEntry{Alias: "big", ServiceType: "vllm", MinSizeBytes: 60 << 30, IsActive: true})
	if got.Status != StatusTight {
		t.Fatalf("status %s (%s), want tight", got.Status, got.Reason)
	}
	if strings.Contains(got.Reason, "Apple") || !strings.Contains(got.Reason, "shared") {
		t.Errorf("reason %q, want a note that unified memory is shared, without Apple Silicon", got.Reason)
	}
}

func TestTheReportCallsAGB10NVIDIAUnifiedMemory(t *testing.T) {
	hw := &Hardware{OS: "linux", Arch: "arm64", SystemRAMBytes: gb10Total}
	if err := nvidiaHardware(hw, gb10CSV, "13.0", gb10Mem); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteTable(&buf, Report{Hardware: *hw}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Unified memory: yes (NVIDIA)") || strings.Contains(out, "Apple Silicon") {
		t.Errorf("report:\n%s\nwant \"Unified memory: yes (NVIDIA)\" and no Apple Silicon", out)
	}
	if !strings.Contains(out, "Memory free:") {
		t.Errorf("report:\n%s\nwant the free memory", out)
	}
}
