package engine

import (
	"context"
	"time"
)

// engineCoreTitle is the process title vLLM gives its engine process.
const engineCoreTitle = "VLLM::EngineCore"

// Orphan is a vLLM engine process whose API server is gone, stopped before an
// engine start (engine start memory plan, E4).
type Orphan struct {
	PID      int
	RSSBytes int64
	Age      time.Duration
	// Forced is true when it ignored SIGTERM and was killed.
	Forced bool
}

// String is the line the console, the log and the dashboard show.
func (o Orphan) String() string { return "" }

// process is one row of the process table.
type process struct {
	PID      int
	PPID     int
	UID      int
	RSSBytes int64
	Age      time.Duration
	Command  string
}

// orphanSweep finds and stops orphaned engine processes.
type orphanSweep struct {
	list   func(context.Context) ([]process, error)
	signal func(pid int, kill bool) error
	self   int
	uid    int
	// only, when set, limits the processes stopped (tests).
	only  func(pid int) bool
	grace time.Duration
	poll  time.Duration
}

func (s orphanSweep) run(ctx context.Context) ([]Orphan, error) { return nil, nil }

// parsePS reads `ps -A -o pid=,ppid=,uid=,rss=,etime=,command=`.
func parsePS(out string) ([]process, error) { return nil, nil }

// parseElapsed reads ps's etime, [[dd-]hh:]mm:ss.
func parseElapsed(s string) (time.Duration, error) { return 0, nil }
