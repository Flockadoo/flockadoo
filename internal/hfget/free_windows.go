//go:build windows

package hfget

// FreeBytes reports the bytes free to this user on the filesystem holding path.
func FreeBytes(path string) (uint64, error) { return 0, nil }
