package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// engineCoreTitle is the process title vLLM gives its engine process.
const engineCoreTitle = "VLLM::EngineCore"

// killWait is how long an orphan has to go after SIGKILL.
const killWait = 5 * time.Second

// Orphan is a vLLM engine process whose API server is gone, stopped before an
// engine start (engine start memory plan, E4).
type Orphan struct {
	PID      int
	RSSBytes int64
	Age      time.Duration
	// Forced is true when it ignored SIGTERM and was killed.
	Forced bool
}

// Summary is "pid 41467, 5.8 GiB, running 7 days 18 hours".
func (o Orphan) Summary() string {
	s := fmt.Sprintf("pid %d, %.1f GiB, running %s", o.PID, float64(o.RSSBytes)/(1<<30), formatAge(o.Age))
	if o.Forced {
		s += ", killed with SIGKILL after it ignored SIGTERM"
	}
	return s
}

// String is the line the console and the log show.
func (o Orphan) String() string {
	return "Stopped an orphaned vLLM engine process (" + o.Summary() + ")."
}

// formatAge is "7 days 18 hours", "3 hours 5 minutes", "4 minutes" or "9 seconds".
func formatAge(d time.Duration) string {
	plural := func(n int64, unit string) string {
		if n == 1 {
			return "1 " + unit
		}
		return strconv.FormatInt(n, 10) + " " + unit + "s"
	}
	pair := func(big int64, bigUnit string, small int64, smallUnit string) string {
		if small == 0 {
			return plural(big, bigUnit)
		}
		return plural(big, bigUnit) + " " + plural(small, smallUnit)
	}
	switch {
	case d >= 24*time.Hour:
		return pair(int64(d/(24*time.Hour)), "day", int64(d%(24*time.Hour)/time.Hour), "hour")
	case d >= time.Hour:
		return pair(int64(d/time.Hour), "hour", int64(d%time.Hour/time.Minute), "minute")
	case d >= time.Minute:
		return plural(int64(d/time.Minute), "minute")
	default:
		return plural(int64(d/time.Second), "second")
	}
}

// process is one row of the process table.
type process struct {
	PID      int
	PPID     int
	UID      int
	RSSBytes int64
	Age      time.Duration
	Command  string
}

func isEngineCore(command string) bool {
	return strings.HasPrefix(strings.TrimSpace(command), engineCoreTitle)
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

// findOrphans picks the engine processes of uid whose API server is gone.
// vLLM's engine process is a child of its API server. When the API server
// dies, the engine process is adopted by pid 1 or, on a Linux desktop, by the
// user's systemd (a subreaper), which is an ancestor of this client. An API
// server is never an ancestor of the client, so an engine process whose
// parent is pid 1 or one of the client's ancestors has no API server.
func findOrphans(procs []process, self, uid int) []process {
	byPID := make(map[int]process, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	adopters := map[int]bool{1: true}
	for pid, steps := self, 0; pid > 1 && steps <= len(procs); steps++ {
		adopters[pid] = true
		p, ok := byPID[pid]
		if !ok {
			break
		}
		pid = p.PPID
	}
	var out []process
	for _, p := range procs {
		if p.PID <= 1 || p.PID == self || p.UID != uid || !isEngineCore(p.Command) {
			continue
		}
		if adopters[p.PPID] {
			out = append(out, p)
		}
	}
	return out
}

func (s orphanSweep) run(ctx context.Context) ([]Orphan, error) {
	procs, err := s.list(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}
	targets := findOrphans(procs, s.self, s.uid)
	if s.only != nil {
		var kept []process
		for _, p := range targets {
			if s.only(p.PID) {
				kept = append(kept, p)
			}
		}
		targets = kept
	}
	if len(targets) == 0 {
		return nil, nil
	}

	var errs []error
	for _, p := range targets {
		if err := s.signal(p.PID, false); err != nil {
			errs = append(errs, fmt.Errorf("SIGTERM to pid %d: %w", p.PID, err))
		}
	}
	forced := map[int]bool{}
	if left := s.waitGone(ctx, targets, s.grace); len(left) > 0 {
		for _, p := range left {
			if err := s.signal(p.PID, true); err != nil {
				errs = append(errs, fmt.Errorf("SIGKILL to pid %d: %w", p.PID, err))
			}
			forced[p.PID] = true
		}
		for _, p := range s.waitGone(ctx, left, killWait) {
			errs = append(errs, fmt.Errorf("orphaned vLLM engine process %d is still running after SIGKILL", p.PID))
			delete(forced, p.PID)
			targets = removePID(targets, p.PID)
		}
	}

	stopped := make([]Orphan, 0, len(targets))
	for _, p := range targets {
		stopped = append(stopped, Orphan{PID: p.PID, RSSBytes: p.RSSBytes, Age: p.Age, Forced: forced[p.PID]})
	}
	return stopped, errors.Join(errs...)
}

func removePID(procs []process, pid int) []process {
	out := procs[:0:0]
	for _, p := range procs {
		if p.PID != pid {
			out = append(out, p)
		}
	}
	return out
}

// waitGone waits up to timeout for procs to exit and returns those still
// running. A pid now held by another program counts as gone: the orphan
// exited and the pid was reused.
func (s orphanSweep) waitGone(ctx context.Context, procs []process, timeout time.Duration) []process {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(s.poll)
	defer tick.Stop()
	left := procs
	for {
		if now, err := s.list(ctx); err == nil {
			left = stillRunning(left, now)
			if len(left) == 0 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return left
		case <-deadline.C:
			return left
		case <-tick.C:
		}
	}
}

func stillRunning(procs, now []process) []process {
	var out []process
	for _, p := range procs {
		for _, q := range now {
			if q.PID == p.PID && q.UID == p.UID && isEngineCore(q.Command) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// parsePS reads `ps -A -o pid=,ppid=,uid=,rss=,etime=,command=`: rss is in
// KiB, etime is [[dd-]hh:]mm:ss. A line it cannot read is skipped.
func parsePS(out string) ([]process, error) {
	var procs []process
	lines := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines++
		var f [5]string
		rest := strings.TrimLeft(line, " \t")
		ok := true
		for i := range f {
			end := strings.IndexAny(rest, " \t")
			if end < 0 {
				ok = false
				break
			}
			f[i], rest = rest[:end], strings.TrimLeft(rest[end:], " \t")
		}
		if !ok {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		uid, err3 := strconv.Atoi(f[2])
		rss, err4 := strconv.ParseInt(f[3], 10, 64)
		age, err5 := parseElapsed(f[4])
		if err := errors.Join(err1, err2, err3, err4, err5); err != nil {
			continue
		}
		procs = append(procs, process{PID: pid, PPID: ppid, UID: uid, RSSBytes: rss * 1024, Age: age, Command: strings.TrimSpace(rest)})
	}
	if lines > 0 && len(procs) == 0 {
		return nil, errors.New("ps printed no process this client can read")
	}
	return procs, nil
}

// parseElapsed reads ps's etime, [[dd-]hh:]mm:ss.
func parseElapsed(s string) (time.Duration, error) {
	var days int
	rest := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil || d < 0 {
			return 0, fmt.Errorf("elapsed time %q", s)
		}
		days, rest = d, s[i+1:]
	}
	parts := strings.Split(rest, ":")
	if len(parts) < 2 || len(parts) > 3 || (days > 0 || strings.Contains(s, "-")) && len(parts) != 3 {
		return 0, fmt.Errorf("elapsed time %q", s)
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return 0, fmt.Errorf("elapsed time %q", s)
		}
		n[3-len(parts)+i] = v
	}
	return time.Duration(days)*24*time.Hour + time.Duration(n[0])*time.Hour + time.Duration(n[1])*time.Minute + time.Duration(n[2])*time.Second, nil
}
