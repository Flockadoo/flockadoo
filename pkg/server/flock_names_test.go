package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sentnl/inferoute-node/inferoute-client/internal/config"
)

// The provider's own screens (the rebrand plan, stage 4; docs/naming.md in
// internal-docs): the terminal view, the local dashboard and the status API
// say which flock this machine is in. The stored role (single, master,
// worker) is shown as Solo, Team · master or Team · worker, the dashboard's
// labels; the status API keeps the stored value. "Provider URL" is the
// Platform URL, "Provider Type" is the Engine. Retired names are built from
// parts so that the rename and the retired-names check pass over this file.

var flockLabels = map[string]string{"single": "Solo", "master": "Team · master", "worker": "Team · worker"}

// withRole gives s a config with this machine's role set through the config
// file, so the test names no Go field.
func withRole(t *testing.T, s *Server, role string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(p, s.config); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "provider:\n", "provider:\n    flock_role: "+role+"\n", 1)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	s.config = cfg
}

var ansi = regexp.MustCompile("\033\\[[0-9;]*m")

// row is the value the console prints after label, or "" when it prints no
// such row.
func row(console, label string) string {
	for _, l := range strings.Split(ansi.ReplaceAllString(console, ""), "\n") {
		if rest, ok := strings.CutPrefix(l, label); ok && strings.HasPrefix(rest, "  ") {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func TestTheConsoleShowsTheFlock(t *testing.T) {
	oldSection := "CLUS" + "TER STATUS"
	for role, label := range flockLabels {
		t.Run(role, func(t *testing.T) {
			s := newTestServer("http://unused", &fakeLLM{})
			withRole(t, s, role)
			console := consoleOutput(t, s)
			plain := ansi.ReplaceAllString(console, "")
			for _, want := range []string{"FLOCKADOO PROVIDER CLIENT", "FLOCK STATUS"} {
				if !strings.Contains(plain, want) {
					t.Errorf("no %q in the console:\n%s", want, plain)
				}
			}
			if strings.Contains(plain, oldSection) {
				t.Errorf("the console still has the old section:\n%s", plain)
			}
			if got := row(console, "Flock"); got != label {
				t.Errorf("Flock row = %q, want %q:\n%s", got, label, plain)
			}
			if got := row(console, "Platform URL"); got == "" {
				t.Errorf("no Platform URL row:\n%s", plain)
			}
			if got := row(console, "Engine"); got == "" {
				t.Errorf("no Engine row (the engine kind):\n%s", plain)
			}
			for _, gone := range []string{"Provider URL", "Provider Type"} {
				if row(console, gone) != "" {
					t.Errorf("the console still has a %s row:\n%s", gone, plain)
				}
			}
			status := statusJSON(t, s)
			if status["flock"] != role {
				t.Errorf("status API flock = %v, want %q", status["flock"], role)
			}
			if _, ok := status["clus"+"ter"]; ok {
				t.Errorf("the status API still has the old field")
			}
		})
	}
}

func TestTheDashboardShowsTheFlock(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	rec := httptest.NewRecorder()
	s.handleDashboard(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	page := rec.Body.String()
	for _, want := range []string{
		"<title>Flockadoo Provider Client</title>", "FLOCKADOO PROVIDER CLIENT", "FLOCK STATUS",
		`"Solo"`, `"Team · master"`, `"Team · worker"`, `row("Flock"`, `row("Platform URL"`, `row("Engine"`, `row("Winglink IP"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the dashboard has no %s", want)
		}
	}
	for _, gone := range []string{"CLUS" + "TER STATUS", `row("Provider URL"`, `row("Provider Type"`, "Cloudflare Tunnel URL"} {
		if strings.Contains(page, gone) {
			t.Errorf("the dashboard still has %s", gone)
		}
	}
}
