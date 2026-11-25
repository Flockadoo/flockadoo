package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Flockadoo/flockadoo/internal/config"
	"github.com/Flockadoo/flockadoo/internal/testenv/fakegateway"
)

// The browser dashboard and the terminal console are two views of one status.
// A provider who opens either must see the same rows in the same order; the
// dashboard used to leave out the Platform connection row and put Benchmark
// and Capabilities under FLOCK STATUS.

// lockedBuffer is a console a running server can redraw while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// lastFrame is the most recent full redraw.
func (b *lockedBuffer) lastFrame() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	frames := strings.Split(b.buf.String(), "\033[H\033[2J")
	return frames[len(frames)-1]
}

// runningConsole redraws a started server's console and returns that frame.
// The console writer is set before Start, so no test write races the server.
func runningConsole(s *Server) string {
	s.redrawConsole()
	return s.console.(*lockedBuffer).lastFrame()
}

// gatewayServer starts s against a fake gateway that accepts serverKey, with
// the client sending clientKey.
func gatewayServer(t *testing.T, serverKey, clientKey string) (*Server, *fakegateway.Gateway) {
	t.Helper()
	gw := fakegateway.New(t, serverKey)
	cfg := &config.Config{}
	cfg.Provider.URL = gw.URL
	cfg.Provider.APIKey = clientKey
	cfg.Provider.Engine = "vllm"
	cfg.Server.Host = "127.0.0.1"
	s := &Server{config: cfg, ctx: context.Background(), console: &lockedBuffer{}}
	errc := make(chan error, 1)
	go func() { errc <- s.Start() }()
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	select {
	case err := <-errc:
		t.Fatalf("Start returned %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	return s, gw
}

// awaitConnection waits until the status API's platform connection satisfies ok.
func awaitConnection(t *testing.T, s *Server, ok func(string) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := statusJSON(t, s)["platform_connection"].(string)
		if ok(got) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("platform_connection = %q after 5s", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The status API carries the Platform connection the console prints, with the
// same text, and flags it as failed only when the tunnel has stopped for good.
func TestThePlatformConnectionIsTheSameInBothViews(t *testing.T) {
	cases := []struct {
		name       string
		server     func(t *testing.T) *Server
		want       func(string) bool
		wantFailed bool
	}{
		{
			name:   "no tunnel yet",
			server: func(*testing.T) *Server { return newTestServer("http://unused", &fakeLLM{}) },
			want:   func(s string) bool { return s == "connecting" },
		},
		{
			name: "an enrolled worker",
			server: func(t *testing.T) *Server {
				s := newTestServer("http://unused", &fakeLLM{})
				withRole(t, s, "worker")
				s.config.Provider.NodeToken = "node-token"
				return s
			},
			want: func(s string) bool { return s == "worker" },
		},
		{
			name: "connected",
			server: func(t *testing.T) *Server {
				s, gw := gatewayServer(t, "the-right-key", "the-right-key")
				gw.AwaitSession(t)
				awaitConnection(t, s, func(c string) bool { return c == "connected" })
				return s
			},
			want: func(s string) bool { return s == "connected" },
		},
		{
			name: "a rejected key",
			server: func(t *testing.T) *Server {
				s, _ := gatewayServer(t, "the-right-key", "a-revoked-key")
				awaitConnection(t, s, func(c string) bool { return strings.HasPrefix(c, "rejected: ") })
				return s
			},
			want:       func(s string) bool { return strings.HasPrefix(s, "rejected: ") && len(s) > len("rejected: ") },
			wantFailed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.server(t)
			status := statusJSON(t, s)
			api, _ := status["platform_connection"].(string)
			if !tc.want(api) {
				t.Errorf("/api/status platform_connection = %q", api)
			}
			failed, _ := status["platform_connection_failed"].(bool)
			if failed != tc.wantFailed {
				t.Errorf("/api/status platform_connection_failed = %v, want %v", failed, tc.wantFailed)
			}
			var console string
			if _, running := s.console.(*lockedBuffer); running {
				console = runningConsole(s)
			} else {
				console = consoleOutput(t, s)
			}
			if console := row(console, "Platform connection"); console != api {
				t.Errorf("console Platform connection = %q, status API = %q", console, api)
			}
		})
	}
}

// The dashboard draws the Platform connection row from the status API, red
// when it failed, in place of the old error-only Connection row.
func TestTheDashboardShowsThePlatformConnection(t *testing.T) {
	page := dashboardPage(t)
	for _, want := range []string{`row("Platform connection"`, "s.platform_connection", "s.platform_connection_failed"} {
		if !strings.Contains(page, want) {
			t.Errorf("the dashboard has no %s", want)
		}
	}
	if strings.Contains(page, `row("Connection"`) {
		t.Error("the dashboard still has the error-only Connection row")
	}
}

// consoleOrder is the order the console prints its rows in. Benchmark and
// Capabilities sit under Session Status, above Engine, not under FLOCK STATUS.
var consoleOrder = []string{
	"Last Health Update",
	"Session Status",
	"Not serving",
	"Engine not ready",
	"Benchmark",
	"Capabilities",
	"Orphaned engines",
	"Update",
	"Engine",
	"Memory share",
	"Provider API Key",
	"Platform URL",
	"LLM URL",
	"Platform connection",
	"FLOCK STATUS",
	"Flock",
	"Nodes",
	"Winglink IP",
	"Model",
	"GPU INFORMATION",
	"GPU",
}

// The console prints its rows in consoleOrder. This holds before the change;
// it pins the order the dashboard is held to below.
func TestTheConsoleRowsAreInOrder(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	s.SetEngineNotReady("model needs about 24.03 GiB to start")
	s.SetEngineNote("verified for 1 concurrent")
	s.SetCapabilities("completed")
	s.SetOrphanedEngines("found at start: 1 vLLM engine process with no API server")
	s.SetMemoryShare("0.81 of 121.86 GiB, chosen at start")
	console := ansi.ReplaceAllString(consoleOutput(t, s), "")
	last, lastLabel := -1, ""
	for _, label := range consoleOrder {
		// Rows the console prints only for some machines are not drawn here.
		switch label {
		case "Not serving", "Update", "Nodes", "Winglink IP":
			continue
		}
		// Labels are padded to 30 columns: two spaces end a label, so
		// "Engine" does not match the start of "Engine not ready".
		i := strings.Index(console, "\n"+label+"  ")
		if label == "FLOCK STATUS" || label == "GPU INFORMATION" {
			i = strings.Index(console, label)
		}
		if i < 0 {
			t.Fatalf("the console has no %q row:\n%s", label, console)
		}
		if i < last {
			t.Errorf("the console prints %q before %q:\n%s", label, lastLabel, console)
		}
		last, lastLabel = i, label
	}
}

// The dashboard draws the rows it shares with the console in the console's
// order.
func TestTheDashboardRowsFollowTheConsole(t *testing.T) {
	page := dashboardPage(t)
	render := page[strings.Index(page, "function render("):]
	marks := map[string]string{
		"Update":          "${updateHtml}",
		"FLOCK STATUS":    "<h2>FLOCK STATUS</h2>",
		"GPU INFORMATION": "<h2>GPU INFORMATION</h2>",
		"Model":           "${modelHtml}",
	}
	last, lastLabel := -1, ""
	for _, label := range consoleOrder {
		mark, ok := marks[label]
		if !ok {
			mark = `row("` + label + `"`
		}
		i := strings.Index(render, mark)
		if i < 0 {
			t.Errorf("the dashboard has no %s", mark)
			continue
		}
		if i < last {
			t.Errorf("the dashboard draws %q before %q; the console has it after", lastLabel, label)
		}
		last, lastLabel = i, label
	}
}

func dashboardPage(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	newTestServer("http://unused", &fakeLLM{}).handleDashboard(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Body.String()
}
