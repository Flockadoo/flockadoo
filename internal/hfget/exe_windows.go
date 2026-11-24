//go:build windows

package hfget

import "path/filepath"

// venvExe is a program in the venv: Scripts\<name>.exe on Windows.
func venvExe(dir, name string) string { return filepath.Join(dir, "Scripts", name+".exe") }
