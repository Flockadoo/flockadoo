package server

import (
	"strings"
	"testing"
)

// Engine start memory plan, item E4. When the start stopped orphaned engine
// processes, the status API, the console and the dashboard say so, in an
// Engine cleanup row under Capabilities.

const cleanupNote = "stopped 1 orphaned vLLM engine process at start: pid 41467, 5.8 GiB, running 7 days 18 hours"

func TestTheEngineCleanupIsInTheStatusAPIAndTheConsole(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	s.SetEngineCleanup(cleanupNote)
	if got, _ := statusJSON(t, s)["engine_cleanup"].(string); got != cleanupNote {
		t.Errorf("status engine_cleanup = %q, want %q", got, cleanupNote)
	}
	console := ansi.ReplaceAllString(consoleOutput(t, s), "")
	if !strings.Contains(console, "Engine cleanup") || !strings.Contains(console, cleanupNote) {
		t.Errorf("the console has no Engine cleanup row:\n%s", console)
	}
}

// Negative: a start that stopped nothing shows no row and no field.
func TestWithoutCleanupThereIsNoRow(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	if _, ok := statusJSON(t, s)["engine_cleanup"]; ok {
		t.Error("status has engine_cleanup with nothing cleaned up")
	}
	if console := ansi.ReplaceAllString(consoleOutput(t, s), ""); strings.Contains(console, "Engine cleanup") {
		t.Errorf("the console has an Engine cleanup row with nothing cleaned up:\n%s", console)
	}
}

func TestTheDashboardDrawsTheEngineCleanup(t *testing.T) {
	if page := dashboardPage(t); !strings.Contains(page, `row("Engine cleanup", esc(s.engine_cleanup))`) {
		t.Error(`the dashboard has no row("Engine cleanup", esc(s.engine_cleanup))`)
	}
}
