//go:build !windows

package hfget

import "golang.org/x/sys/unix"

// FreeBytes reports the bytes free to this user on the filesystem holding path.
func FreeBytes(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:unconvert // the field types differ between linux and darwin, so one of these conversions is needed on each
}
