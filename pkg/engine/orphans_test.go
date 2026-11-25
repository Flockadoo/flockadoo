package engine

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Engine start memory plan, item E4. vLLM runs an API server and an engine
// process (VLLM::EngineCore). A hard kill of the API server skips vLLM's
// shutdown, and the engine process keeps running with its memory: on the
// macmini one was 7 days 18 hours old, held about 6 GB of 16 GB, and made the
// next start fail with Metal out-of-memory. Before it starts an engine, the
// client stops this user's engine processes whose API server is gone, and says
// so (decision D3). It never touches an engine whose API server is alive, or
// another user's processes.
//
// An orphan is adopted by pid 1 (macOS, servers) or, on a Linux desktop, by
// the user's systemd, which is an ancestor of the client. Either way its new
// parent is pid 1 or one of the client's own ancestors; an API server never is.

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
		// A live engine: its API server is running.
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

// fakeProcesses is a process table that signals act on.
type fakeProcesses struct {
	mu         sync.Mutex
	procs      []process
	ignoreTerm map[int]bool
	// reuse, when set, replaces a pid with another program once it is sent SIGTERM.
	reuse   map[int]process
	signals []string
	listErr error
}

func (f *fakeProcesses) list(context.Context) ([]process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return slices.Clone(f.procs), nil
}

func (f *fakeProcesses) signal(pid int, kill bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := "TERM"
	if kill {
		name = "KILL"
	}
	f.signals = append(f.signals, name+" "+strconv.Itoa(pid))
	if !kill && f.ignoreTerm[pid] {
		return nil
	}
	f.procs = slices.DeleteFunc(f.procs, func(p process) bool { return p.PID == pid })
	if p, ok := f.reuse[pid]; ok && !kill {
		f.procs = append(f.procs, p)
	}
	return nil
}

func (f *fakeProcesses) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.signals)
}

func sweepOf(f *fakeProcesses) orphanSweep {
	return orphanSweep{list: f.list, signal: f.signal, self: selfPID, uid: uid, grace: 200 * time.Millisecond, poll: 5 * time.Millisecond}
}

func pids(orphans []Orphan) []int {
	var out []int
	for _, o := range orphans {
		out = append(out, o.PID)
	}
	slices.Sort(out)
	return out
}

func TestOrphanedEngineProcessesAreStopped(t *testing.T) {
	f := &fakeProcesses{procs: processTable()}
	stopped, err := sweepOf(f).run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := pids(stopped); !slices.Equal(got, []int{7002, 41467}) {
		t.Fatalf("stopped %v, want 7002 and 41467", got)
	}
	sent := f.sent()
	slices.Sort(sent)
	if !slices.Equal(sent, []string{"TERM 41467", "TERM 7002"}) {
		t.Errorf("signals %v, want SIGTERM to the two orphans only", sent)
	}
	for _, o := range stopped {
		if o.Forced {
			t.Errorf("%d was killed; it stopped on SIGTERM", o.PID)
		}
	}
	if o := stopped[slices.IndexFunc(stopped, func(o Orphan) bool { return o.PID == 41467 })]; o.RSSBytes != 6_082_560*1024 || o.Age != 7*day+18*time.Hour+23*time.Minute {
		t.Errorf("41467 reported as %+v, want its memory and age", o)
	}
}

// Negative: an engine process whose API server is alive may be serving (it may
// be another vLLM the operator runs); it is never touched.
func TestAnEngineWhoseAPIServerIsAliveIsNotTouched(t *testing.T) {
	f := &fakeProcesses{procs: processTable()}
	if _, err := sweepOf(f).run(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, s := range f.sent() {
		if strings.HasSuffix(s, " 6001") || strings.HasSuffix(s, " 6000") {
			t.Errorf("signalled the live engine: %s", s)
		}
	}
}

// Negative: another user's processes and other programs are never touched.
func TestOtherUsersAndOtherProgramsAreNotTouched(t *testing.T) {
	f := &fakeProcesses{procs: processTable()}
	if _, err := sweepOf(f).run(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, s := range f.sent() {
		if strings.HasSuffix(s, " 7003") || strings.HasSuffix(s, " 7004") {
			t.Errorf("signalled %s", s)
		}
	}
}

func TestAnOrphanThatIgnoresSIGTERMIsKilled(t *testing.T) {
	f := &fakeProcesses{procs: processTable(), ignoreTerm: map[int]bool{41467: true}}
	stopped, err := sweepOf(f).run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.sent(), "KILL 41467") {
		t.Errorf("signals %v, want SIGKILL to 41467 after the grace period", f.sent())
	}
	if slices.Contains(f.sent(), "KILL 7002") {
		t.Errorf("signals %v: 7002 stopped on SIGTERM and was killed anyway", f.sent())
	}
	if len(stopped) != 2 {
		t.Fatalf("stopped %+v, want both orphans", stopped)
	}
	for _, o := range stopped {
		if o.Forced != (o.PID == 41467) {
			t.Errorf("%d forced %v", o.PID, o.Forced)
		}
	}
}

// Negative: a pid that, after SIGTERM, belongs to another program (the orphan
// exited and the pid was reused) is never sent SIGKILL.
func TestAReusedPIDIsNotKilled(t *testing.T) {
	f := &fakeProcesses{
		procs: processTable(),
		reuse: map[int]process{41467: {PID: 41467, PPID: 4000, UID: uid, Command: "/usr/bin/vim notes.txt"}},
	}
	stopped, err := sweepOf(f).run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.sent(), "KILL 41467") {
		t.Errorf("signals %v: the reused pid was killed", f.sent())
	}
	if got := pids(stopped); !slices.Equal(got, []int{7002, 41467}) {
		t.Errorf("stopped %v, want both orphans reported", got)
	}
}

func TestWithoutOrphansNothingIsSignalled(t *testing.T) {
	table := slices.DeleteFunc(processTable(), func(p process) bool { return p.PID == 41467 || p.PID == 7002 })
	f := &fakeProcesses{procs: table}
	stopped, err := sweepOf(f).run(t.Context())
	if err != nil || len(stopped) != 0 || len(f.sent()) != 0 {
		t.Errorf("run = %v, %v; signals %v; want nothing", stopped, err, f.sent())
	}
}

// A process table that cannot be read is an error; nothing is signalled.
func TestAnUnreadableProcessTableIsAnError(t *testing.T) {
	f := &fakeProcesses{listErr: errors.New("ps: not found")}
	if _, err := sweepOf(f).run(t.Context()); err == nil {
		t.Error("run = nil, want the listing error")
	}
	if len(f.sent()) != 0 {
		t.Errorf("signals %v", f.sent())
	}
}

func TestTheReportNamesPIDMemoryAndAge(t *testing.T) {
	o := Orphan{PID: 41467, RSSBytes: 6_082_560 * 1024, Age: 7*day + 18*time.Hour + 23*time.Minute}
	line := o.String()
	for _, want := range []string{"orphaned vLLM engine process", "41467", "5.8 GiB", "7 days 18 hours"} {
		if !strings.Contains(line, want) {
			t.Errorf("%q does not say %q", line, want)
		}
	}
	forced := Orphan{PID: 7002, RSSBytes: 2 << 30, Age: 3 * time.Hour, Forced: true}.String()
	for _, want := range []string{"7002", "2.0 GiB", "3 hours", "SIGKILL"} {
		if !strings.Contains(forced, want) {
			t.Errorf("%q does not say %q", forced, want)
		}
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
