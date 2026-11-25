//go:build !windows

package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// FindOrphans lists this user's vLLM engine processes whose API server is
// gone (engine start memory plan, E4). It reads the process table and changes
// nothing.
func FindOrphans(ctx context.Context) ([]Orphan, error) {
	return orphanScan{list: psProcesses, self: os.Getpid(), uid: os.Getuid()}.run(ctx)
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
