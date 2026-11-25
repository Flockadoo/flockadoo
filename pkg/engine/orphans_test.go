package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// Engine start memory plan, item E4. vLLM runs an API server and an engine
// process (VLLM::EngineCore). A hard kill of the API server skips vLLM's
// shutdown, and the engine process keeps running with its memory: on the
// macmini one was 7 days 18 hours old, held about 6 GB of 16 GB, and made the
// next start fail with Metal out-of-memory. Before it starts an engine, the
// client reports this user's engine processes whose API server is gone, with
// how to stop one. It never stops a process: the operator decides (decision
// D3, changed to report only on 2026-11-25). An engine whose API server is
// alive, however it was started, is not reported.
//
// An orphan is adopted by pid 1 (macOS, servers) or, on a Linux desktop, by
// the user's systemd, which is an ancestor of the client. Either way its new
// parent is pid 1 or one of the client's own ancestors; an API server is not.

const (
	selfPID = 5000
	uid     = 1000
	day     = 24 * time.Hour
)

func processTable() []process {
	return []process{
		{PID: 1, PPID: 0, UID: 0, Command: "/sbin/init"},
		{PID: 900, PPID: 1, UID: uid, Command: "/usr/lib/systemd/systemd --user"},
		{PID: 4000, PPID: 900, UID: uid, Command: "-bash"},
		{PID: selfPID, PPID: 4000, UID: uid, Command: "flockadoo"},
		// A vLLM the operator started outside Flockadoo: its API server is running.
		{PID: 6000, PPID: 4000, UID: uid, Command: "/home/u/.venv-vllm/bin/python /home/u/.venv-vllm/bin/vllm serve org/m"},
		{PID: 6001, PPID: 6000, UID: uid, RSSBytes: 3 << 30, Age: time.Hour, Command: "VLLM::EngineCore"},
		// Orphans: adopted by init, and by the user's systemd.
		{PID: 41467, PPID: 1, UID: uid, RSSBytes: 6_082_560 * 1024, Age: 7*day + 18*time.Hour + 23*time.Minute, Command: "VLLM::EngineCore"},
		{PID: 7002, PPID: 900, UID: uid, RSSBytes: 2 << 30, Age: 3 * time.Hour, Command: "VLLM::EngineCore                "},
		// Another user's orphan.
		{PID: 7003, PPID: 1, UID: 1001, RSSBytes: 1 << 30, Command: "VLLM::EngineCore"},
		// Not an engine process.
		{PID: 7004, PPID: 1, UID: uid, Command: "/usr/bin/python3 worker.py"},
	}
}

func scanOf(procs []process, err error) orphanScan {
	return orphanScan{
		list: func(context.Context) ([]process, error) { return slices.Clone(procs), err },
		self: selfPID,
		uid:  uid,
	}
}

func pids(orphans []Orphan) []int {
	var out []int
	for _, o := range orphans {
		out = append(out, o.PID)
	}
	slices.Sort(out)
	return out
}

func TestOrphanedEngineProcessesAreFound(t *testing.T) {
	found, err := scanOf(processTable(), nil).run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := pids(found); !slices.Equal(got, []int{7002, 41467}) {
		t.Fatalf("found %v, want 7002 and 41467", got)
	}
	o := found[slices.IndexFunc(found, func(o Orphan) bool { return o.PID == 41467 })]
	if o.RSSBytes != 6_082_560*1024 || o.Age != 7*day+18*time.Hour+23*time.Minute {
		t.Errorf("41467 reported as %+v, want its memory and age", o)
	}
}

// Negative: a vLLM whose API server is running is not reported, whoever
// started it; nor are other users' processes or other programs.
func TestRunningEnginesOtherUsersAndOtherProgramsAreNotReported(t *testing.T) {
	found, err := scanOf(processTable(), nil).run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range found {
		switch o.PID {
		case 6000, 6001, 7003, 7004:
			t.Errorf("reported pid %d", o.PID)
		}
	}
}

func TestWithoutOrphansNothingIsReported(t *testing.T) {
	table := slices.DeleteFunc(processTable(), func(p process) bool { return p.PID == 41467 || p.PID == 7002 })
	found, err := scanOf(table, nil).run(t.Context())
	if err != nil || len(found) != 0 {
		t.Errorf("run = %v, %v; want nothing", found, err)
	}
}

func TestAnUnreadableProcessTableIsAnError(t *testing.T) {
	if _, err := scanOf(nil, errors.New("ps: not found")).run(t.Context()); err == nil {
		t.Error("run = nil, want the listing error")
	}
}

// The report says what the process is, what it holds, and how to stop it; it
// does not say it was stopped.
func TestTheReportNamesPIDMemoryAgeAndHowToStopIt(t *testing.T) {
	line := Orphan{PID: 41467, RSSBytes: 6_082_560 * 1024, Age: 7*day + 18*time.Hour + 23*time.Minute}.String()
	for _, want := range []string{"vLLM engine process", "no API server", "41467", "5.8 GiB", "7 days 18 hours", "kill 41467"} {
		if !strings.Contains(line, want) {
			t.Errorf("%q does not say %q", line, want)
		}
	}
	if strings.Contains(strings.ToLower(line), "stopped") {
		t.Errorf("%q says the process was stopped", line)
	}
	if got := (Orphan{PID: 7002, RSSBytes: 2 << 30, Age: 3 * time.Hour}).Summary(); got != "pid 7002, 2.0 GiB, running 3 hours" {
		t.Errorf("Summary = %q", got)
	}
}

// Found in R on the macmini (2026-11-25): macOS had paged out an orphan
// (pid 84833, 6 hours 43 minutes old), so ps gave it almost no resident
// memory and the report said "holding memory (pid 84833, 0.0 GiB, ...)". Its
// memory is in the compressor or swap, not gone: the report says it is paged
// out instead of 0.0 GiB.
func TestAPagedOutOrphanIsNotReportedAsHoldingNoMemory(t *testing.T) {
	o := Orphan{PID: 84833, RSSBytes: 3 << 20, Age: 6*time.Hour + 43*time.Minute}
	if got := o.Summary(); got != "pid 84833, memory paged out, running 6 hours 43 minutes" {
		t.Errorf("Summary = %q", got)
	}
	if line := o.String(); strings.Contains(line, "0.0 GiB") {
		t.Errorf("%q says 0.0 GiB", line)
	}
	// Negative: an orphan with memory resident still gives its size.
	if got := (Orphan{PID: 84833, RSSBytes: 1288 << 20, Age: 6*time.Hour + 40*time.Minute}).Summary(); got != "pid 84833, 1.3 GiB, running 6 hours 40 minutes" {
		t.Errorf("Summary = %q", got)
	}
}

// The process table as ps prints it on macOS and Linux (pid, ppid, uid, rss in
// KiB, elapsed time, command).
func TestPSOutputIsParsed(t *testing.T) {
	out := `    1     0     0  12345    7-18:23:45 /sbin/launchd
41467     1   501 6082560 7-18:23:45 VLLM::EngineCore
  812   600   501   2048       05:03 /bin/zsh -l
  900   812   501   4096    01:02:03 /Users/c/.venv-vllm-metal/bin/python /Users/c/.venv-vllm-metal/bin/vllm serve baai/bge-m3 --port 8000
`
	got, err := parsePS(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []process{
		{PID: 1, PPID: 0, UID: 0, RSSBytes: 12345 * 1024, Age: 7*day + 18*time.Hour + 23*time.Minute + 45*time.Second, Command: "/sbin/launchd"},
		{PID: 41467, PPID: 1, UID: 501, RSSBytes: 6082560 * 1024, Age: 7*day + 18*time.Hour + 23*time.Minute + 45*time.Second, Command: "VLLM::EngineCore"},
		{PID: 812, PPID: 600, UID: 501, RSSBytes: 2048 * 1024, Age: 5*time.Minute + 3*time.Second, Command: "/bin/zsh -l"},
		{PID: 900, PPID: 812, UID: 501, RSSBytes: 4096 * 1024, Age: time.Hour + 2*time.Minute + 3*time.Second, Command: "/Users/c/.venv-vllm-metal/bin/python /Users/c/.venv-vllm-metal/bin/vllm serve baai/bge-m3 --port 8000"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("parsePS =\n%+v\nwant\n%+v", got, want)
	}
}

func TestElapsedTimesAreParsed(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"00:07":       7 * time.Second,
		"05:03":       5*time.Minute + 3*time.Second,
		"1:02:03":     time.Hour + 2*time.Minute + 3*time.Second,
		"7-18:23:45":  7*day + 18*time.Hour + 23*time.Minute + 45*time.Second,
		"12-00:00:00": 12 * day,
	} {
		got, err := parseElapsed(in)
		if err != nil || got != want {
			t.Errorf("parseElapsed(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "1:2:3:4", "x-01:00:00"} {
		if _, err := parseElapsed(bad); err == nil {
			t.Errorf("parseElapsed(%q) = nil error", bad)
		}
	}
}
