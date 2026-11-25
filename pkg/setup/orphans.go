package setup

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Flockadoo/flockadoo/pkg/engine"
)

// scanOrphans finds orphaned engine processes before an engine start (engine
// start memory plan, E4). It never stops them. Tests replace it.
var scanOrphans = engine.FindOrphans

// StartReport is what the engine start found that the dashboard shows.
type StartReport struct {
	Orphans []engine.Orphan
	// MemoryShare is the share vLLM was started with (E2).
	MemoryShare ShareChoice
	// NotReady is why the engine is not ready after the start (a model that
	// does not fit, say); empty when the start went well. The console clears
	// what the start printed, so the log and the dashboard carry it.
	NotReady string
}

// Note is the dashboard's Orphaned engines line; empty when none were found.
func (r StartReport) Note() string {
	if hint := orphanHint(r.Orphans); hint != "" {
		return "found at start: " + hint
	}
	return ""
}

// orphanHint says which orphaned engine processes hold memory and how to stop
// them; empty when there are none.
func orphanHint(orphans []engine.Orphan) string {
	n := len(orphans)
	if n == 0 {
		return ""
	}
	summaries := make([]string, 0, n)
	pids := make([]string, 0, n)
	for _, o := range orphans {
		summaries = append(summaries, o.Summary())
		pids = append(pids, strconv.Itoa(o.PID))
	}
	what, it := "1 vLLM engine process with no API server is", "it"
	if n > 1 {
		what, it = fmt.Sprintf("%d vLLM engine processes with no API server are", n), "them"
	}
	return fmt.Sprintf("%s holding memory (%s); stop %s with: kill %s",
		what, strings.Join(summaries, "; "), it, strings.Join(pids, " "))
}

// reportOrphans runs scan before an engine start and says what it found. A
// scan that fails is reported and the start goes on.
func reportOrphans(ctx context.Context, out io.Writer, scan func(context.Context) ([]engine.Orphan, error)) []engine.Orphan {
	found, err := scan(ctx)
	for _, o := range found {
		_, _ = fmt.Fprintln(out, o.String())
	}
	if err != nil {
		_, _ = fmt.Fprintf(out, "Could not check for orphaned vLLM engine processes (%v); starting anyway.\n", err)
	}
	return found
}
