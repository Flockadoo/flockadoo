package compat

import "testing"

// Plan item Q3: nvidia-smi's CSV is output the client did not write.
func FuzzSplitCSV(f *testing.F) {
	f.Add("NVIDIA GeForce RTX 4090, 24564, 550.54.15")
	f.Add(`"quoted, name", 1`)
	f.Fuzz(func(t *testing.T, line string) {
		_ = splitCSV(line)
	})
}
