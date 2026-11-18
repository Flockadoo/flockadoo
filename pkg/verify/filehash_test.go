package verify

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
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

// Plan item Q3, finding SEC-5: the header length is read from the file, so a
// file that claims a huge header must be refused, not allocated.
func TestAHugeSafetensorsHeaderLengthIsRefused(t *testing.T) {
	for _, n := range []uint64{1 << 62, maxSafetensorsHeader + 1} {
		path := writeBytes(t, "huge.safetensors", binary.LittleEndian.AppendUint64(nil, n))
		_, err := FileHash(path, "safetensors_header")
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("a header length of %d gave %v, want a too-large error", n, err)
		}
	}
}

// Plan item Q3: the header reader reads bytes the client did not write.
// Whatever a file holds, it returns a hash or an error and never panics, and
// a hash means the header fitted the bound.
func FuzzSafetensorsHeader(f *testing.F) {
	f.Add(safetensors(`{"meta":{},"tensors":{}}`, []byte("weights")))
	f.Add(binary.LittleEndian.AppendUint64(nil, 1<<62))
	f.Add([]byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, b []byte) {
		path := filepath.Join(t.TempDir(), "f.safetensors")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := FileHash(path, "safetensors_header"); err == nil && len(b) >= 8 &&
			binary.LittleEndian.Uint64(b[:8]) > maxSafetensorsHeader {
			t.Fatalf("accepted a header of %d bytes", binary.LittleEndian.Uint64(b[:8]))
		}
	})
}
