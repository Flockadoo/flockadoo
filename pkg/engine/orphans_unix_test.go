//go:build !windows

package engine

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// E4 on real processes: a fake API server (this test binary) starts a child
// titled VLLM::EngineCore, as vLLM does, and is killed with SIGKILL, so the
// child is orphaned exactly as on the macmini. The sweep reads the real
// process table with ps and signals real processes. It is limited to the
// processes the test started, so a real engine on the machine running the
// tests is never touched.

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
		if os.Getenv("FLOCKADOO_ORPHAN_IGNORE_TERM") == "1" {
			signal.Ignore(syscall.SIGTERM)
		}
		// Never outlive a broken test by long.
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
}

// startFakeEngine starts a fake API server and returns it with its engine
// process's pid.
func startFakeEngine(t *testing.T, ignoreTerm bool) (*exec.Cmd, int) {
	t.Helper()
	api := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestOrphanHelperProcess$")
	api.Env = append(os.Environ(), helperEnv+"=api")
	if ignoreTerm {
		api.Env = append(api.Env, "FLOCKADOO_ORPHAN_IGNORE_TERM=1")
	}
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

func realSweep(core int, grace time.Duration) orphanSweep {
	return orphanSweep{
		list:   psProcesses,
		signal: signalProcess,
		self:   os.Getpid(),
		uid:    os.Getuid(),
		only:   func(pid int) bool { return pid == core },
		grace:  grace,
		poll:   20 * time.Millisecond,
	}
}

func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func awaitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !gone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestARealOrphanedEngineProcessIsStopped(t *testing.T) {
	api, core := startFakeEngine(t, false)
	orphan(t, api, core)

	stopped, err := realSweep(core, 5*time.Second).run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 1 || stopped[0].PID != core || stopped[0].Forced {
		t.Fatalf("stopped %+v, want engine process %d stopped by SIGTERM", stopped, core)
	}
	if stopped[0].RSSBytes <= 0 {
		t.Errorf("memory %d, want the process's resident memory", stopped[0].RSSBytes)
	}
	awaitGone(t, core)
}

func TestARealOrphanThatIgnoresSIGTERMIsKilled(t *testing.T) {
	api, core := startFakeEngine(t, true)
	orphan(t, api, core)

	stopped, err := realSweep(core, 500*time.Millisecond).run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 1 || stopped[0].PID != core || !stopped[0].Forced {
		t.Fatalf("stopped %+v, want engine process %d killed after SIGTERM was ignored", stopped, core)
	}
	awaitGone(t, core)
}

// Negative: an engine process whose API server is alive is left running.
func TestARealEngineWithItsAPIServerIsLeftRunning(t *testing.T) {
	_, core := startFakeEngine(t, false)

	stopped, err := realSweep(core, 5*time.Second).run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 0 {
		t.Errorf("stopped %+v, want nothing: the API server is alive", stopped)
	}
	if gone(core) {
		t.Errorf("engine process %d was stopped", core)
	}
}
