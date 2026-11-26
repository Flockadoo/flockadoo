package compat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/Flockadoo/flockadoo/internal/hardware"
	"github.com/Flockadoo/flockadoo/pkg/exechide"
)

// MemoryKind describes which memory pool the scorer should use.
type MemoryKind string

const (
	MemoryVRAM    MemoryKind = "vram"
	MemoryUnified MemoryKind = "unified"
	MemorySystem  MemoryKind = "system"
)

// Hardware is the local machine snapshot used for compatibility scoring.
type Hardware struct {
	OS               string     `json:"os"`
	Arch             string     `json:"arch"`
	ProductName      string     `json:"product_name"`
	DriverVersion    string     `json:"driver_version,omitempty"`
	CUDAVersion      string     `json:"cuda_version,omitempty"`
	GPUCount         int        `json:"gpu_count"`
	MemoryKind       MemoryKind `json:"memory_kind"`
	MemoryTotalBytes int64      `json:"memory_total_bytes"`
	MemoryFreeBytes  int64      `json:"memory_free_bytes"`
	MemoryUsedBytes  int64      `json:"memory_used_bytes"`
	SystemRAMBytes   int64      `json:"system_ram_bytes"`
	UsableBytes      int64      `json:"usable_bytes"`
	UnifiedMemory    bool       `json:"unified_memory"`
	Warnings         []string   `json:"warnings,omitempty"`
}

// Detect probes local hardware for Linux/Windows NVIDIA and macOS (Apple Silicon/Intel).
// It never hard-fails solely because a GPU is missing; CPU/system-RAM fallbacks are used.
func Detect(ctx context.Context) (*Hardware, error) {
	hw := &Hardware{
		OS:   runtime.GOOS,   //nolint:forbidigo // detection records the host platform (PLAT-2, item H1)
		Arch: runtime.GOARCH, //nolint:forbidigo // detection records the host platform (PLAT-2, item H1)
	}

	ram, err := systemRAMBytes()
	if err != nil {
		hw.Warnings = append(hw.Warnings, fmt.Sprintf("system RAM detection failed: %v", err))
	} else {
		hw.SystemRAMBytes = ram
	}

	switch hw.OS {
	case "darwin":
		if err := detectDarwin(ctx, hw); err != nil {
			hw.Warnings = append(hw.Warnings, err.Error())
			fallbackSystemMemory(hw, "macOS GPU detection failed; using system RAM")
		}
	case "linux", "windows":
		if err := detectNVIDIA(ctx, hw); err != nil {
			hw.Warnings = append(hw.Warnings, err.Error())
			fallbackSystemMemory(hw, "NVIDIA GPU not detected; scoring against system RAM (slow/CPU path)")
		}
	default:
		fallbackSystemMemory(hw, fmt.Sprintf("unsupported OS %s; using system RAM if available", hw.OS))
	}

	if hw.UsableBytes <= 0 && hw.SystemRAMBytes > 0 {
		fallbackSystemMemory(hw, "usable memory unset; falling back to system RAM")
	}

	return hw, nil
}

func fallbackSystemMemory(hw *Hardware, warning string) {
	if warning != "" {
		hw.Warnings = append(hw.Warnings, warning)
	}
	if hw.ProductName == "" {
		hw.ProductName = "CPU / unknown GPU"
	}
	hw.MemoryKind = MemorySystem
	hw.UnifiedMemory = false
	hw.MemoryTotalBytes = hw.SystemRAMBytes
	hw.MemoryFreeBytes = 0
	hw.MemoryUsedBytes = 0
	// Leave headroom for OS and other processes.
	hw.UsableBytes = int64(float64(hw.SystemRAMBytes) * 0.70)
	if hw.GPUCount == 0 {
		hw.GPUCount = 0
	}
}

// appleSiliconUsableFraction is a conservative share of unified memory for model weights + runtime.
const appleSiliconUsableFraction = 0.65

// nvidiaUnifiedUsableFraction is the share of an NVIDIA unified-memory
// machine's memory (GB10) scored as usable (hardware.NvidiaUnifiedUsableFraction,
// which the GPU report sends as the machine's memory).
const nvidiaUnifiedUsableFraction = hardware.NvidiaUnifiedUsableFraction

func detectDarwin(ctx context.Context, hw *Hardware) error {
	productName, coreCount, err := parseSystemProfilerGPU(ctx)
	if err != nil {
		return err
	}
	hw.ProductName = productName
	if coreCount > 0 {
		hw.ProductName = fmt.Sprintf("%s (%d cores)", productName, coreCount)
	}
	hw.DriverVersion = "macOS Native"
	hw.CUDAVersion = "N/A"
	hw.GPUCount = 1

	isAppleSilicon := hw.Arch == "arm64" ||
		strings.Contains(strings.ToLower(productName), "apple")

	if isAppleSilicon {
		hw.MemoryKind = MemoryUnified
		hw.UnifiedMemory = true
		hw.MemoryTotalBytes = hw.SystemRAMBytes
		hw.UsableBytes = int64(float64(hw.SystemRAMBytes) * appleSiliconUsableFraction)
		if hw.SystemRAMBytes <= 0 {
			return fmt.Errorf("Apple Silicon detected but system RAM is unknown") //nolint:staticcheck // ST1005: the message starts with a proper noun
		}
		vmStat, err := exec.CommandContext(ctx, "/usr/bin/vm_stat").Output()
		setMacFreeMemory(hw, vmStat, err)
		return nil
	}

	// Intel Mac: discrete/shared GPU memory is unreliable via system_profiler; use system RAM.
	fallbackSystemMemory(hw, "Intel Mac GPU VRAM not exposed; scoring against system RAM")
	hw.ProductName = productName
	return nil
}

func parseSystemProfilerGPU(ctx context.Context) (productName string, coreCount int, err error) {
	if _, lookErr := exec.LookPath("system_profiler"); lookErr != nil {
		return "", 0, fmt.Errorf("system_profiler not found: %w", lookErr)
	}
	cmd := exec.CommandContext(ctx, "system_profiler", "SPDisplaysDataType")
	output, err := cmd.Output()
	if err != nil {
		return "", 0, fmt.Errorf("system_profiler: %w", err)
	}

	lines := strings.Split(string(output), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "Chipset Model:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				productName = strings.TrimSpace(parts[1])
			}
		}
		if strings.Contains(line, "Total Number of Cores:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				if n, atoiErr := strconv.Atoi(strings.TrimSpace(parts[1])); atoiErr == nil {
					coreCount = n
				}
			}
		}
		if productName == "" && i > 0 && strings.HasSuffix(strings.TrimSpace(lines[i-1]), ":") {
			productName = strings.TrimSuffix(strings.TrimSpace(lines[i-1]), ":")
		}
	}
	if productName == "" {
		productName = "Unknown macOS GPU"
	}
	return productName, coreCount, nil
}

func detectNVIDIA(ctx context.Context, hw *Hardware) error {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return fmt.Errorf("nvidia-smi not found: %w", err)
	}
	cmd := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name,driver_version,memory.total,memory.used,memory.free,uuid", "--format=csv,noheader,nounits")
	exechide.Apply(cmd)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("nvidia-smi: %w", err)
	}
	return nvidiaHardware(hw, string(output), queryCUDAVersion(ctx), systemMemInfo)
}

// memInfo is the system's memory: MemTotal and MemAvailable on Linux.
type memInfo = hardware.MemInfo

// parseMemInfo reads MemTotal and MemAvailable from /proc/meminfo's format
// (hardware.ParseMemInfo, which the GPU report reads too).
func parseMemInfo(r io.Reader) (memInfo, error) { return hardware.ParseMemInfo(r) }

// nvidiaHardware fills hw from nvidia-smi's CSV (name, driver, total, used,
// free, uuid, in MiB). mem reads the system's memory.
//
// A GPU that reports no memory of its own ([N/A] or [Not Supported], as an
// NVIDIA GB10 does) shares the system's memory with the CPU: it is unified
// memory, like Apple Silicon, measured from the system (engine start memory
// plan, E1). GPUs that report their memory are scored on their VRAM.
func nvidiaHardware(hw *Hardware, csv, cudaVersion string, mem func() (memInfo, error)) error {
	type gpuRow struct {
		name   string
		driver string
		total  int64
		used   int64
		free   int64
	}
	var gpus, unreported []gpuRow
	for _, line := range strings.Split(strings.TrimSpace(csv), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := splitCSV(line)
		if len(parts) < 5 {
			continue
		}
		totalMiB, errTotal := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
		usedMiB, _ := strconv.ParseInt(strings.TrimSpace(parts[3]), 10, 64)
		freeMiB, _ := strconv.ParseInt(strings.TrimSpace(parts[4]), 10, 64)
		if errTotal != nil {
			unreported = append(unreported, gpuRow{name: strings.TrimSpace(parts[0]), driver: strings.TrimSpace(parts[1])})
			continue
		}
		gpus = append(gpus, gpuRow{
			name:   strings.TrimSpace(parts[0]),
			driver: strings.TrimSpace(parts[1]),
			total:  totalMiB * 1024 * 1024,
			used:   usedMiB * 1024 * 1024,
			free:   freeMiB * 1024 * 1024,
		})
	}
	if len(gpus) == 0 && len(unreported) > 0 {
		g := unreported[0]
		m, err := mem()
		if err != nil {
			return fmt.Errorf("%s reports no memory of its own (unified memory) and the system's memory could not be read: %w", g.name, err)
		}
		hw.ProductName = g.name
		hw.DriverVersion = g.driver
		hw.CUDAVersion = cudaVersion
		hw.GPUCount = len(unreported)
		hw.MemoryKind = MemoryUnified
		hw.UnifiedMemory = true
		hw.MemoryTotalBytes = m.Total
		hw.MemoryFreeBytes = m.Available
		hw.MemoryUsedBytes = m.Total - m.Available
		hw.UsableBytes = int64(float64(m.Total) * nvidiaUnifiedUsableFraction)
		return nil
	}
	if len(gpus) == 0 {
		return fmt.Errorf("nvidia-smi returned no GPUs")
	}

	// v1: score against the largest single GPU VRAM.
	best := gpus[0]
	for _, g := range gpus[1:] {
		if g.total > best.total {
			best = g
		}
	}

	hw.ProductName = best.name
	hw.DriverVersion = best.driver
	hw.CUDAVersion = cudaVersion
	hw.GPUCount = len(gpus)
	hw.MemoryKind = MemoryVRAM
	hw.UnifiedMemory = false
	hw.MemoryTotalBytes = best.total
	hw.MemoryUsedBytes = best.used
	hw.MemoryFreeBytes = best.free
	hw.UsableBytes = best.total

	if best.total > 0 && float64(best.free)/float64(best.total) < 0.50 {
		hw.Warnings = append(hw.Warnings,
			fmt.Sprintf("free VRAM (%.1f GiB) is much lower than total (%.1f GiB); models may not load until memory is freed",
				bytesToGiB(best.free), bytesToGiB(best.total)))
	}
	if len(gpus) > 1 {
		hw.Warnings = append(hw.Warnings,
			fmt.Sprintf("%d GPUs detected; scoring against largest single GPU (%.1f GiB)", len(gpus), bytesToGiB(best.total)))
	}
	return nil
}

// unifiedLabel names the kind of unified memory a machine has.
func (hw *Hardware) unifiedLabel() string {
	if strings.HasPrefix(hw.ProductName, "NVIDIA") {
		return "NVIDIA"
	}
	return "Apple Silicon"
}

func queryCUDAVersion(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "nvidia-smi")
	exechide.Apply(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "CUDA Version:") {
			idx := strings.Index(line, "CUDA Version:")
			rest := strings.TrimSpace(line[idx+len("CUDA Version:"):])
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return ""
}

func splitCSV(line string) []string {
	var parts []string
	var cur strings.Builder
	inQuotes := false
	for _, r := range line {
		switch {
		case r == '"':
			inQuotes = !inQuotes
		case r == ',' && !inQuotes:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	parts = append(parts, cur.String())
	return parts
}

func bytesToGiB(b int64) float64 {
	return float64(b) / (1024 * 1024 * 1024)
}

var (
	vmStatPageSize = regexp.MustCompile(`page size of (\d+) bytes`)
	vmStatCounters = []struct {
		name string
		re   *regexp.Regexp
	}{
		{"free", regexp.MustCompile(`(?m)^Pages free:\s+(\d+)\.`)},
		{"inactive", regexp.MustCompile(`(?m)^Pages inactive:\s+(\d+)\.`)},
		{"speculative", regexp.MustCompile(`(?m)^Pages speculative:\s+(\d+)\.`)},
	}
)

// macFreeMemory reads a Mac's free memory from /usr/bin/vm_stat's output:
// free, inactive and speculative pages. That is psutil's available memory on
// macOS, which vLLM Metal sizes its KV cache to, so the client and the engine
// agree on what is free. Output without any of them gives an error, not a
// guess.
func macFreeMemory(vmStat string) (int64, error) {
	m := vmStatPageSize.FindStringSubmatch(vmStat)
	if m == nil {
		return 0, errors.New("vm_stat output has no page size")
	}
	pageSize, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || pageSize <= 0 {
		return 0, fmt.Errorf("vm_stat page size %q", m[1])
	}
	var pages int64
	for _, c := range vmStatCounters {
		m := c.re.FindStringSubmatch(vmStat)
		if m == nil {
			return 0, fmt.Errorf("vm_stat output has no %s pages", c.name)
		}
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("vm_stat %s pages %q", c.name, m[1])
		}
		pages += n
	}
	return pages * pageSize, nil
}

// setMacFreeMemory sets a Mac's free and used memory from vm_stat's output.
// When vm_stat cannot run or be read, the Mac has no free reading (the start's
// fit check is then skipped) and a warning says why.
func setMacFreeMemory(hw *Hardware, vmStat []byte, runErr error) {
	free, err := macFreeMemory(string(vmStat))
	if runErr != nil {
		err = runErr
	}
	if err != nil {
		hw.Warnings = append(hw.Warnings, "free memory not read (vm_stat: "+err.Error()+")")
		return
	}
	hw.MemoryFreeBytes = free
	if hw.MemoryTotalBytes > free {
		hw.MemoryUsedBytes = hw.MemoryTotalBytes - free
	}
}
