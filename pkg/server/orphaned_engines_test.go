package server

import (
	"strings"
	"testing"
)

// Engine start memory plan, item E4. When the start found vLLM engine
// processes whose API server is gone, the status API, the console and the
// dashboard say so, in an Orphaned engines row under Capabilities.

const orphanNote = "found at start: 1 vLLM engine process with no API server is holding memory (pid 41467, 5.8 GiB, running 7 days 18 hours); stop it with: kill 41467"

func TestOrphanedEnginesAreInTheStatusAPIAndTheConsole(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	s.SetOrphanedEngines(orphanNote)
	if got, _ := statusJSON(t, s)["orphaned_engines"].(string); got != orphanNote {
		t.Errorf("status orphaned_engines = %q, want %q", got, orphanNote)
	}
	console := ansi.ReplaceAllString(consoleOutput(t, s), "")
	if !strings.Contains(console, "Orphaned engines") || !strings.Contains(console, orphanNote) {
		t.Errorf("the console has no Orphaned engines row:\n%s", console)
	}
}

// Negative: a start that found nothing shows no row and no field.
func TestWithoutOrphansThereIsNoRow(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	if _, ok := statusJSON(t, s)["orphaned_engines"]; ok {
		t.Error("status has orphaned_engines with none found")
	}
	if console := ansi.ReplaceAllString(consoleOutput(t, s), ""); strings.Contains(console, "Orphaned engines") {
		t.Errorf("the console has an Orphaned engines row with none found:\n%s", console)
	}
}

func TestTheDashboardDrawsOrphanedEngines(t *testing.T) {
	if page := dashboardPage(t); !strings.Contains(page, `row("Orphaned engines", esc(s.orphaned_engines), "warn")`) {
		t.Error(`the dashboard has no row("Orphaned engines", esc(s.orphaned_engines), "warn")`)
	}
}
