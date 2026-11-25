//go:build !windows

package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// StopOrphans finds this user's vLLM engine processes whose API server is
// gone and stops them, before an engine start (engine start memory plan, E4):
// SIGTERM, then SIGKILL after 15 seconds. It returns those it stopped.
func StopOrphans(ctx context.Context) ([]Orphan, error) {
	return orphanSweep{
		list:   psProcesses,
		signal: signalProcess,
		self:   os.Getpid(),
		uid:    os.Getuid(),
		grace:  15 * time.Second,
		poll:   250 * time.Millisecond,
	}.run(ctx)
}

// psProcesses reads the process table with ps, which macOS and Linux both have.
func psProcesses(ctx context.Context) ([]process, error) {
	cmd := exec.CommandContext(ctx, "ps", "-A", "-ww", "-o", "pid=,ppid=,uid=,rss=,etime=,command=")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parsePS(string(out))
}

// signalProcess sends SIGTERM, or SIGKILL, to one process; a process already
// gone is not an error. It never signals pid 1 or below: 0 and -1 would reach
// a whole group or every process.
func signalProcess(pid int, kill bool) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal pid %d", pid)
	}
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
