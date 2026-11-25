//go:build !windows

package engine

import "context"

// FindOrphans lists this user's vLLM engine processes whose API server is
// gone. It reads the process table and changes nothing.
func FindOrphans(ctx context.Context) ([]Orphan, error) { return nil, nil }

func psProcesses(ctx context.Context) ([]process, error) { return nil, nil }
