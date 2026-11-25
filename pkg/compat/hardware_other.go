//go:build !darwin && !linux && !windows

package compat

import "fmt"

func systemRAMBytes() (int64, error) {
	return 0, fmt.Errorf("system RAM detection not implemented on this OS")
}

// systemMemInfo is read only on Linux, the one platform with NVIDIA unified
// memory (GB10).
func systemMemInfo() (memInfo, error) {
	return memInfo{}, fmt.Errorf("available memory is read only on Linux")
}
