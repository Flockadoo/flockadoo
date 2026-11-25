package setup

import (
	"context"
	"fmt"
	"io"
	"strings"

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
func (r StartReport) Note() string {
	n := len(r.StoppedOrphans)
	if n == 0 {
		return ""
	}
	summaries := make([]string, 0, n)
	for _, o := range r.StoppedOrphans {
		summaries = append(summaries, o.Summary())
	}
	noun := "process"
	if n > 1 {
		noun = "processes"
	}
	return fmt.Sprintf("stopped %d orphaned vLLM engine %s at start: %s", n, noun, strings.Join(summaries, "; "))
}

// sweepOrphans runs sweep before an engine start and says what it stopped. A
// sweep that fails is reported and the start goes on.
func sweepOrphans(ctx context.Context, out io.Writer, sweep func(context.Context) ([]engine.Orphan, error)) []engine.Orphan {
	stopped, err := sweep(ctx)
	for _, o := range stopped {
		_, _ = fmt.Fprintln(out, o.String())
	}
	if err != nil {
		_, _ = fmt.Fprintf(out, "Could not check for orphaned vLLM engine processes (%v); starting anyway.\n", err)
	}
	return stopped
}
