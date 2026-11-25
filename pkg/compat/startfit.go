package compat

import "github.com/Flockadoo/flockadoo/pkg/verify"

// DoesNotFit is a model that needs more memory to start than is free for the
// engine (engine start memory plan, E3).
type DoesNotFit struct {
	Model   string
	Need    int64
	Free    int64
	Context int64
	Kind    MemoryKind
}

func (e *DoesNotFit) Error() string { return "" }

// StartCheck compares what a model needs to start (its weights plus one
// request at its full context) with the memory free for the engine now.
func StartCheck(hw *Hardware, entry verify.CatalogEntry, maxModelLen int64) error { return nil }
