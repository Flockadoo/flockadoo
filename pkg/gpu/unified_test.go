package gpu

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/hardware"
)

// The unified memory plan (flockadoo-plans, item C1; decisions D1–D3). A GB10
// has no memory of its own: nvidia-smi reports it as N/A, and the health
// report sent 0, so the dashboard showed nothing. The report now says what
// the machine can use, and how it is held:
//
//   - a GB10 sends total = 0.80 of MemTotal (the share it is scored with),
//     used = MemTotal − MemAvailable capped at that total, free = the rest,
//     and memory_kind "unified";
//   - a Mac sends memory_kind "unified" with the figures it sends today;
//   - a dedicated GPU sends no kind, and its figures do not change;
//   - a GB10 whose memory cannot be read sends 0 and no kind, as before.
//
// The checks read the report's JSON, which is what the server receives.

// peipei's MemTotal and MemAvailable on 2026-11-24 (as compat's E1 test):
// 121.86 GiB total, 105.25 GiB available.
const peipeiMemInfo = "MemTotal:       127779096 kB\nMemAvailable:   110362624 kB\n"

func memFrom(text string) func() (hardware.MemInfo, error) {
	return func() (hardware.MemInfo, error) { return hardware.ParseMemInfo(strings.NewReader(text)) }
}

// reported is the GPU block as the health report encodes it.
func reported(t *testing.T, info *GPUInfo) map[string]any {
	t.Helper()
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func wantMemory(t *testing.T, got map[string]any, total, used, free float64, kind string) {
	t.Helper()
	if got["memory_total"] != total || got["memory_used"] != used || got["memory_free"] != free {
		t.Errorf("memory total/used/free = %v/%v/%v MiB, want %v/%v/%v",
			got["memory_total"], got["memory_used"], got["memory_free"], total, used, free)
	}
	k, present := got["memory_kind"]
	switch {
	case kind == "" && present:
		t.Errorf("memory_kind = %v, want no field", k)
	case kind != "" && k != kind:
		t.Errorf("memory_kind = %v, want %q", k, kind)
	}
}

func TestAGB10ReportsItsUsableUnifiedMemory(t *testing.T) {
	info, err := nvidiaGPUInfo(nvidiaDoc("NVIDIA GB10", "N/A", "N/A", "N/A"), memFrom(peipeiMemInfo))
	if err != nil {
		t.Fatal(err)
	}
	// total: 0.80 × 127,779,096 kB = 99,827.4 MiB, rounded down.
	// used: 127,779,096 − 110,362,624 kB = 17,008.3 MiB, rounded down.
	wantMemory(t, reported(t, info), 99827, 17008, 99827-17008, "unified")
}

// When the system uses more than the usable share, used is the share and
// nothing is free: used never exceeds the total the machine reports.
func TestAGB10sUsedMemoryIsCappedAtItsUsableShare(t *testing.T) {
	busy := "MemTotal:       127779096 kB\nMemAvailable:    10000000 kB\n"
	info, err := nvidiaGPUInfo(nvidiaDoc("NVIDIA GB10", "N/A", "N/A", "N/A"), memFrom(busy))
	if err != nil {
		t.Fatal(err)
	}
	wantMemory(t, reported(t, info), 99827, 99827, 0, "unified")
}

// Negative: a dedicated GPU's memory is its own. It is not marked, its
// figures are nvidia-smi's, and the system's memory is not read.
func TestADedicatedGPUReportsNoMemoryKind(t *testing.T) {
	read := false
	mem := func() (hardware.MemInfo, error) {
		read = true
		return hardware.ParseMemInfo(strings.NewReader(peipeiMemInfo))
	}
	info, err := nvidiaGPUInfo(nvidiaDoc("NVIDIA GeForce RTX 4090", "24564 MiB", "20000 MiB", "4564 MiB"), mem)
	if err != nil {
		t.Fatal(err)
	}
	wantMemory(t, reported(t, info), 24564, 20000, 4564, "")
	if read {
		t.Error("the system's memory was read for a GPU with memory of its own")
	}
}

// Negative: a GB10 whose memory cannot be read reports nothing, as before,
// and is not marked: there is no figure to label.
func TestAGB10WithoutMemInfoReportsNothing(t *testing.T) {
	mem := func() (hardware.MemInfo, error) { return hardware.MemInfo{}, errors.New("no /proc/meminfo") }
	info, err := nvidiaGPUInfo(nvidiaDoc("NVIDIA GB10", "N/A", "N/A", "N/A"), mem)
	if err != nil {
		t.Fatal(err)
	}
	wantMemory(t, reported(t, info), 0, 0, 0, "")
}

// Apple Silicon is unified memory too. Its figures are Metal's, as today.
func TestAMacReportsUnifiedMemoryWithItsFiguresUnchanged(t *testing.T) {
	const gib = int64(1) << 30
	info := macGPUInfo("Apple M4 (10 cores)", macStats{Utilization: 31, UsedBytes: 6 * gib, TotalBytes: 16 * gib}, 4.5, 41)
	got := reported(t, info)
	wantMemory(t, got, 16384, 6144, 10240, "unified")
	if got["product_name"] != "Apple M4 (10 cores)" || got["utilization"] != 31.0 || got["is_busy"] != true {
		t.Errorf("the rest of the Mac's report changed: %v", got)
	}
}
