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

// Orphan is a vLLM engine process whose API server is gone (engine start
// memory plan, E4). The client reports it and never stops it: the operator
// decides (decision D3, report only).
type Orphan struct {
	PID      int
	RSSBytes int64
	Age      time.Duration
}

// Summary is "pid 41467, 5.8 GiB, running 7 days 18 hours".
func (o Orphan) Summary() string {
	memory := fmt.Sprintf("%.1f GiB", float64(o.RSSBytes)/(1<<30))
	// macOS pages out an idle orphan: ps then gives it almost no resident
	// memory, while its memory sits in the compressor or swap (R, macmini).
	if memory == "0.0 GiB" {
		memory = "memory paged out"
	}
	return fmt.Sprintf("pid %d, %s, running %s", o.PID, memory, formatAge(o.Age))
}

// String is the line the console and the log show.
func (o Orphan) String() string {
	return fmt.Sprintf("A vLLM engine process with no API server is holding memory (%s). It is probably left over from a vLLM that was killed. If it is not yours to keep, stop it with: kill %d", o.Summary(), o.PID)
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

// orphanScan finds orphaned engine processes.
type orphanScan struct {
	list func(context.Context) ([]process, error)
	self int
	uid  int
	// only, when set, limits the processes reported (tests).
	only func(pid int) bool
}

// run picks the engine processes of uid whose API server is gone. vLLM's
// engine process is a child of its API server. When the API server dies, the
// engine process is adopted by pid 1 or, on a Linux desktop, by the user's
// systemd (a subreaper), which is an ancestor of this client. An API server
// is not an ancestor of the client, so an engine process whose parent is pid 1
// or one of the client's ancestors has no API server.
func (s orphanScan) run(ctx context.Context) ([]Orphan, error) {
	procs, err := s.list(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}
	byPID := make(map[int]process, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	adopters := map[int]bool{1: true}
	for pid, steps := s.self, 0; pid > 1 && steps <= len(procs); steps++ {
		adopters[pid] = true
		p, ok := byPID[pid]
		if !ok {
			break
		}
		pid = p.PPID
	}
	var found []Orphan
	for _, p := range procs {
		if p.PID <= 1 || p.PID == s.self || p.UID != s.uid || !isEngineCore(p.Command) || !adopters[p.PPID] {
			continue
		}
		if s.only != nil && !s.only(p.PID) {
			continue
		}
		found = append(found, Orphan{PID: p.PID, RSSBytes: p.RSSBytes, Age: p.Age})
	}
	return found, nil
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
		if errors.Join(err1, err2, err3, err4, err5) != nil {
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
	bad := fmt.Errorf("elapsed time %q", s)
	days, rest, hasDays := 0, s, false
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil || d < 0 {
			return 0, bad
		}
		days, rest, hasDays = d, s[i+1:], true
	}
	parts := strings.Split(rest, ":")
	if len(parts) < 2 || len(parts) > 3 || hasDays && len(parts) != 3 {
		return 0, bad
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return 0, bad
		}
		n[3-len(parts)+i] = v
	}
	return time.Duration(days)*24*time.Hour + time.Duration(n[0])*time.Hour + time.Duration(n[1])*time.Minute + time.Duration(n[2])*time.Second, nil
}
