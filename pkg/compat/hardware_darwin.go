//go:build darwin

package compat

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func systemRAMBytes() (int64, error) {
	n, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, fmt.Errorf("sysctl hw.memsize: %w", err)
	}
	return int64(n), nil
}

// systemMemInfo is read only on Linux, the one platform with NVIDIA unified
// memory (GB10).
func systemMemInfo() (memInfo, error) {
	return memInfo{}, fmt.Errorf("available memory is read only on Linux")
}
