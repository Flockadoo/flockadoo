//go:build !windows

package engine

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// E4 on real processes: a fake API server (this test binary) starts a child
// titled VLLM::EngineCore, as vLLM does, and is killed with SIGKILL, so the
// child is orphaned exactly as on the macmini. The scan reads the real process
// table with ps. It is limited to the processes the test started.

const helperEnv = "FLOCKADOO_ORPHAN_HELPER"

// TestOrphanHelperProcess is the fake API server and engine process. It does
// nothing when run as a test.
func TestOrphanHelperProcess(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case "api":
		core := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestOrphanHelperProcess$")
		core.Args[0] = engineCoreTitle
		core.Env = append(os.Environ(), helperEnv+"=core")
		if err := core.Start(); err != nil {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString(strconv.Itoa(core.Process.Pid) + "\n")
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	case "core":
		// Never outlive a broken test by long.
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
}

// startFakeEngine starts a fake API server and returns it with its engine
// process's pid. Both are killed when the test ends.
func startFakeEngine(t *testing.T) (*exec.Cmd, int) {
	t.Helper()
	api := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestOrphanHelperProcess$")
	api.Env = append(os.Environ(), helperEnv+"=api")
	out, err := api.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := api.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("reading the engine pid: %v", err)
	}
	core, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(core, syscall.SIGKILL)
		_ = api.Process.Kill()
		_ = api.Wait()
	})
	return api, core
}

// orphan kills the API server with SIGKILL, as the out-of-memory killer
// would, and waits until its engine process has been adopted.
func orphan(t *testing.T, api *exec.Cmd, core int) {
	t.Helper()
	_ = api.Process.Kill()
	_ = api.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// Read on its own, not with the code under test.
		out, err := exec.CommandContext(t.Context(), "ps", "-o", "ppid=", "-p", strconv.Itoa(core)).Output()
		if err != nil {
			t.Fatalf("ps: %v", err)
		}
		if ppid, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && ppid != api.Process.Pid {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("engine process %d was not adopted after its API server died", core)
}

func realScan(core int) orphanScan {
	return orphanScan{list: psProcesses, self: os.Getpid(), uid: os.Getuid(), only: func(pid int) bool { return pid == core }}
}

func running(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// A real orphan is reported, and is still running afterwards: the client
// never stops it.
func TestARealOrphanIsReportedAndLeftRunning(t *testing.T) {
	api, core := startFakeEngine(t)
	orphan(t, api, core)

	found, err := realScan(core).run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].PID != core {
		t.Fatalf("found %+v, want engine process %d", found, core)
	}
	if found[0].RSSBytes <= 0 {
		t.Errorf("memory %d, want the process's resident memory", found[0].RSSBytes)
	}
	if !running(core) {
		t.Errorf("engine process %d was stopped; the client only reports", core)
	}
}

// Negative: an engine process whose API server is running is not reported.
func TestARealEngineWithItsAPIServerIsNotReported(t *testing.T) {
	_, core := startFakeEngine(t)

	found, err := realScan(core).run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("found %+v, want nothing: the API server is running", found)
	}
}
