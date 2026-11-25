//go:build windows

package engine

import "context"

// StopOrphans does nothing on Windows: vLLM does not run there.
func StopOrphans(context.Context) ([]Orphan, error) { return nil, nil }
