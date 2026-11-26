//go:build !linux

package hardware

// ReadMemInfo is read only on Linux, the one platform with NVIDIA unified
// memory (GB10).
func ReadMemInfo() (MemInfo, error) {
	return MemInfo{}, ErrMemInfoUnsupported
}
