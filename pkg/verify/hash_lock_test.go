//go:build unix

package verify

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Hashing a weight runs outside the verifier lock. Verdict takes that lock,
// and a full GGUF or a pytorch shard outlasts the node's request deadline, so
// holding the lock for the hash stalls every request until the hash finishes
// (B3: hashing runs outside the lock a request takes).
func TestHashingAWeightDoesNotBlockAVerdict(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "tokenizer.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	v := NewVerifier(nil, nil, "vllm", "", "")

	done := make(chan error, 1)
	go func() {
		_, _, err := v.measureWithCache("m", dir)
		done <- err
	}()

	// FileHash opens the weight and then reads it. The write side pairs with
	// that open and holds the read until release, so the hash is in progress
	// while the verdict lock is checked.
	release := make(chan struct{})
	opened := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			opened <- err
			return
		}
		opened <- nil
		<-release
		_, werr := f.Write([]byte(`{"ok":true}`))
		cerr := f.Close()
		if werr != nil {
			done <- werr
			return
		}
		if cerr != nil {
			done <- cerr
		}
	}()

	select {
	case err := <-opened:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-done:
		t.Fatalf("hash finished before the weight file was opened: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("hash never opened the weight file")
	}

	held := !v.mu.TryLock()
	if !held {
		v.mu.Unlock()
	}
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hash did not finish")
	}
	if held {
		t.Fatal("verifier lock held while hashing a weight file; a request's verdict would stall until the hash finishes")
	}
}
