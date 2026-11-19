//go:build darwin && cgo

package gpu

import (
	"context"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestMacSensors reads the real sensors on this Mac and prints them.
// Run on the machine itself: go test -run MacSensors -v ./pkg/gpu
func TestMacSensors(t *testing.T) {
	// Power is energy between two samples, so the first call primes it.
	macSensors()
	time.Sleep(2 * time.Second)
	powerW, tempC := macSensors()
	t.Logf("sensors: power_draw_w=%.2f temperature_c=%.1f", powerW, tempC)

	m := &Monitor{isMacOS: true, cacheTTL: gpuInfoCacheTTL}
	info, err := m.GetGPUInfo(context.Background())
	if err != nil {
		t.Fatalf("GetGPUInfo: %v", err)
	}
	t.Logf("gpu info: product=%q utilization=%.0f%% memory_used=%dMiB memory_total=%dMiB memory_free=%dMiB",
		info.ProductName, info.Utilization, info.MemoryUsed, info.MemoryTotal, info.MemoryFree)

	if runtime.GOARCH != "arm64" {
		t.Skip("Intel Mac: IOReport power and IOHID temperature are Apple Silicon only")
	}
	if vm, err := unix.SysctlUint32("kern.hv_vmm_present"); err == nil && vm == 1 {
		// A virtual Mac (CI's macOS runners) has no GPU power or temperature
		// sensor; this test reads real hardware and runs on a real Mac.
		t.Skip("virtual Mac: no GPU power or temperature sensors to read") //nolint:forbidigo // hardware test: a VM has no sensors
	}
	if tempC <= 0 {
		t.Error("no GPU or SoC die temperature sensor found")
	}
	if powerW <= 0 {
		t.Error("no GPU power reading; if the GPU was fully idle, rerun while a model is generating")
	}
	if info.MemoryTotal <= 0 {
		t.Error("memory_total is 0: sysctl hw.memsize not read")
	}
}
