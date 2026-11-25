package setup

import (
	"context"

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
func (c ShareChoice) Note() string { return "" }

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
func shareChoiceFor(cfg *config.Config, kind engine.Kind, args []string, hw *compat.Hardware) ShareChoice {
	return ShareChoice{}
}

// withShare applies a chosen share to spec.
func withShare(spec engine.Spec, c ShareChoice) engine.Spec { return spec }
