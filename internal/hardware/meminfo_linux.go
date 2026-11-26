//go:build linux

package hardware

import "os"

// ReadMemInfo reads /proc/meminfo.
func ReadMemInfo() (MemInfo, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return MemInfo{}, err
	}
	defer func() { _ = f.Close() }()
	return ParseMemInfo(f)
}
