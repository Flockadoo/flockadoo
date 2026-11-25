package setup

import (
	"context"
	"fmt"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/pkg/compat"
	"github.com/Flockadoo/flockadoo/pkg/engine"
)

// ShareChoice is the memory share vLLM starts with on unified memory (engine
// start memory plan, E2). The zero value is none: vLLM's own default.
type ShareChoice struct {
	Share      float64
	TotalBytes int64
	FreeBytes  int64
	// FromCommand is true when the saved engine command sets the share.
	FromCommand bool
}

// Note is the dashboard's Memory share line; empty when there is none.
func (c ShareChoice) Note() string {
	switch {
	case c.Share <= 0:
		return ""
	case c.FromCommand:
		return fmt.Sprintf("%.2f, set in the engine command", c.Share)
	default:
		return fmt.Sprintf("%.2f of %s, chosen at start (%s was free)", c.Share, gib(c.TotalBytes), gib(c.FreeBytes))
	}
}

func gib(b int64) string { return fmt.Sprintf("%.2f GiB", float64(b)/(1<<30)) }

// chooseShare reads the machine's memory and picks the share for a start of
// kind with args. Tests replace it.
var chooseShare = func(ctx context.Context, cfg *config.Config, kind engine.Kind, args []string) ShareChoice {
	hw, err := compat.Detect(ctx)
	if err != nil {
		hw = nil
	}
	return shareChoiceFor(cfg, kind, args, hw)
}

// shareChoiceFor picks the share for a start of kind with args on hw.
// Only vLLM (CUDA) on a Solo machine gets one: vLLM Metal plans its own
// memory (whether it honours the flag is checked in R), llama.cpp takes no
// share, and a flock's memory is planned across machines.
func shareChoiceFor(cfg *config.Config, kind engine.Kind, args []string, hw *compat.Hardware) ShareChoice {
	if kind != engine.KindVLLM {
		return ShareChoice{}
	}
	if role := cfg.Provider.FlockRole; role == "master" || role == "worker" {
		return ShareChoice{}
	}
	if share, ok := engine.MemoryShareOf(args); ok {
		return ShareChoice{Share: share, FromCommand: true}
	}
	if hw == nil || hw.MemoryKind != compat.MemoryUnified || hw.MemoryTotalBytes <= 0 || hw.MemoryFreeBytes <= 0 {
		return ShareChoice{}
	}
	share := engine.MemoryShare(hw.MemoryTotalBytes, hw.MemoryFreeBytes)
	if share <= 0 {
		return ShareChoice{}
	}
	return ShareChoice{Share: share, TotalBytes: hw.MemoryTotalBytes, FreeBytes: hw.MemoryFreeBytes}
}

// withShare applies a chosen share to spec. A share from the command is
// already in it; the chosen one is never saved, so each start chooses again.
func withShare(spec engine.Spec, c ShareChoice) engine.Spec {
	if c.FromCommand || c.Share <= 0 {
		return spec
	}
	return spec.WithMemoryShare(c.Share)
}
