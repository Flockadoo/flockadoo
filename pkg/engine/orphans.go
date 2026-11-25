package engine

import (
	"context"
	"time"
)

// engineCoreTitle is the process title vLLM gives its engine process.
const engineCoreTitle = "VLLM::EngineCore"

// Orphan is a vLLM engine process whose API server is gone (engine start
// memory plan, E4). The client reports it and never stops it.
type Orphan struct {
	PID      int
	RSSBytes int64
	Age      time.Duration
}

// Summary is "pid 41467, 5.8 GiB, running 7 days 18 hours".
func (o Orphan) Summary() string { return "" }

// String is the line the console and the log show.
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

// orphanScan finds orphaned engine processes.
type orphanScan struct {
	list func(context.Context) ([]process, error)
	self int
	uid  int
	// only, when set, limits the processes reported (tests).
	only func(pid int) bool
}

func (s orphanScan) run(ctx context.Context) ([]Orphan, error) { return nil, nil }

// parsePS reads `ps -A -o pid=,ppid=,uid=,rss=,etime=,command=`.
func parsePS(out string) ([]process, error) { return nil, nil }

// parseElapsed reads ps's etime, [[dd-]hh:]mm:ss.
func parseElapsed(s string) (time.Duration, error) { return 0, nil }
