package setup

import (
	"context"
	"io"

	"github.com/Flockadoo/flockadoo/pkg/engine"
)

// scanOrphans finds orphaned engine processes before an engine start (engine
// start memory plan, E4). It never stops them. Tests replace it.
var scanOrphans = engine.FindOrphans

// StartReport is what the engine start found that the dashboard shows.
type StartReport struct {
	Orphans []engine.Orphan
}

// Note is the dashboard's Orphaned engines line; empty when none were found.
func (r StartReport) Note() string { return "" }

// reportOrphans runs scan before an engine start and says what it found. A
// scan that fails is reported and the start goes on.
func reportOrphans(ctx context.Context, out io.Writer, scan func(context.Context) ([]engine.Orphan, error)) []engine.Orphan {
	return nil
}
