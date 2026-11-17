package verify

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// Characterization tests for the per-file hash (plan item Q2): they pin the
// algorithm the node's bootstrap script uses, before the only test that
// reached it (through the dead WeightFingerprint helper) goes with it. The
// expected values were computed independently with Python's hashlib.

func writeBytes(t *testing.T, name string, b []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func safetensors(header string, weights []byte) []byte {
	b := binary.LittleEndian.AppendUint64(nil, uint64(len(header)))
	return append(append(b, header...), weights...)
}

func TestFileHashFullHashesTheWholeFile(t *testing.T) {
	path := writeBytes(t, "config.json", []byte(`{"model_type":"test"}`))
	got, err := FileHash(path, "full")
	if err != nil {
		t.Fatal(err)
	}
	if want := "69e683f430bd66f59c9ef0f4931624f59db5c3246a40a7c6ceffa4661a36d24c"; got != want {
		t.Fatalf("full hash = %s, want %s", got, want)
	}
}

// The header method hashes the 8-byte length and the header JSON, and not
// the weights after it (finding SEC-2: it identifies a layout, not weights).
func TestFileHashSafetensorsHeaderHashesTheLengthAndHeaderOnly(t *testing.T) {
	const want = "154e2a4a5fd0df724efc984a21a2755a9ae04761587b81a2310f2998bfb594f4"
	for _, weights := range [][]byte{nil, []byte("WEIGHTS-NOT-HASHED")} {
		path := writeBytes(t, "model.safetensors", safetensors(`{"meta":{},"tensors":{}}`, weights))
		got, err := FileHash(path, "safetensors_header")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("header hash with %d bytes of weights = %s, want %s", len(weights), got, want)
		}
	}
}

func TestFileHashRefusesWhatItCannotHash(t *testing.T) {
	path := writeBytes(t, "x", []byte("x"))
	if _, err := FileHash(path, "md5"); err == nil {
		t.Error("an unknown hash method was accepted")
	}
	if _, err := FileHash(writeBytes(t, "short.safetensors", []byte{1, 2, 3}), "safetensors_header"); err == nil {
		t.Error("a file shorter than the length prefix was accepted")
	}
	if _, err := FileHash(writeBytes(t, "cut.safetensors", safetensors(`{"meta":{},"tensors":{}}`, nil)[:12]), "safetensors_header"); err == nil {
		t.Error("a truncated header was accepted")
	}
}
