//go:build !windows

package engine

import (
	"context"
)

// StopOrphans finds this user's vLLM engine processes whose API server is
// gone and stops them, before an engine start.
func StopOrphans(ctx context.Context) ([]Orphan, error) { return nil, nil }

func psProcesses(ctx context.Context) ([]process, error) { return nil, nil }

func signalProcess(pid int, kill bool) error { return nil }
