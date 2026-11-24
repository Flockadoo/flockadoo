//go:build !windows

package hfget

import "path/filepath"

// venvExe is a program in the venv: bin/<name> outside Windows.
func venvExe(dir, name string) string { return filepath.Join(dir, "bin", name) }
