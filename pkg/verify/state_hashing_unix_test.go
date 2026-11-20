//go:build !windows

package verify

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// 13. Hashing the weights does not hold up a request or a report. A refresh
// used to hash under the verifier's lock, so for a large .bin (2.2 GB on the
// Mac mini, about 20 seconds) every Verdict and every Status waited for the
// hash. The weight file here is a named pipe: hashing it blocks until the
// test writes to it, which is the slow hash without a slow test. Named pipes
// do not exist on Windows; the lock is the same code on every platform.
func TestHashingDoesNotHoldUpAVerdictOrAReport(t *testing.T) {
	w := newWorld(t, "m")
	w.nodeSays("verified")
	pipe := filepath.Join(w.dir, "pytorch_model.bin")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}

	refreshed := make(chan struct{})
	go func() { w.v.Refresh(context.Background(), "m"); close(refreshed) }()

	// Opening the pipe for writing returns once the hash has opened it to read.
	writer, err := os.OpenFile(pipe, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	finishHash := func() {
		if writer != nil {
			_, _ = writer.Write([]byte("weights"))
			_ = writer.Close()
			writer = nil
		}
	}
	t.Cleanup(finishHash)

	within := func(what string, f func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { f(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			finishHash()
			t.Fatalf("%s waited for the weights to be hashed", what)
		}
	}
	within("a request's verdict", func() { w.v.Verdict("m") })
	within("the health report's status", func() { w.v.Status("m") })

	finishHash()
	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh did not finish after the hash could")
	}
	// Writing to the pipe changed its mtime, so the verdict would see new
	// weights; the recorded answer is what this test checks.
	if st := w.v.Status("m"); st != StatusVerified {
		t.Fatalf("after the hash and the server's answer the status is %q, want %q", st, StatusVerified)
	}
}
