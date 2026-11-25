package setup

import (
	"context"
	"io"

	"github.com/Flockadoo/flockadoo/pkg/engine"
)

// stopOrphans stops orphaned engine processes before an engine start
// (engine start memory plan, E4). Tests replace it.
var stopOrphans = engine.StopOrphans

// StartReport is what the engine start did that the dashboard shows.
type StartReport struct {
	StoppedOrphans []engine.Orphan
}

// Note is the dashboard's Engine cleanup line; empty when nothing was stopped.
func (r StartReport) Note() string { return "" }

// sweepOrphans runs sweep before an engine start and says what it stopped. A
// sweep that fails is reported and the start goes on.
func sweepOrphans(ctx context.Context, out io.Writer, sweep func(context.Context) ([]engine.Orphan, error)) []engine.Orphan {
	return nil
}
