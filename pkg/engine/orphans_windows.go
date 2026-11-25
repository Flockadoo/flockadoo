//go:build windows

package engine

import "context"

// FindOrphans finds nothing on Windows: vLLM does not run there.
func FindOrphans(context.Context) ([]Orphan, error) { return nil, nil }
