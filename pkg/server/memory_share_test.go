package server

import (
	"strings"
	"testing"
)

// Engine start memory plan, item E2. The memory share vLLM was started with on
// unified memory is in the status API and the dashboard, in a Memory share row
// under Engine. The console never shows it (Charles, 2026-11-26).

const shareNote = "0.81 of 121.86 GiB, chosen at start (105.25 GiB was free)"

func TestTheMemoryShareIsInTheStatusAPI(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	s.SetMemoryShare(shareNote)
	if got, _ := statusJSON(t, s)["memory_share"].(string); got != shareNote {
		t.Errorf("status memory_share = %q, want %q", got, shareNote)
	}
}

// Negative: the console shows no Memory share row even when there is a share.
func TestTheConsoleNeverShowsTheMemoryShare(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	s.SetMemoryShare(shareNote)
	console := ansi.ReplaceAllString(consoleOutput(t, s), "")
	if strings.Contains(console, "Memory share") || strings.Contains(console, shareNote) {
		t.Errorf("the console shows the memory share:\n%s", console)
	}
}

// Negative: a start with no share (a dedicated GPU, say) shows no row.
func TestWithoutAShareThereIsNoRow(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	if _, ok := statusJSON(t, s)["memory_share"]; ok {
		t.Error("status has memory_share with no share")
	}
	if console := ansi.ReplaceAllString(consoleOutput(t, s), ""); strings.Contains(console, "Memory share") {
		t.Errorf("the console has a Memory share row with no share:\n%s", console)
	}
}

func TestTheDashboardDrawsTheMemoryShare(t *testing.T) {
	if page := dashboardPage(t); !strings.Contains(page, `row("Memory share", esc(s.memory_share))`) {
		t.Error(`the dashboard has no row("Memory share", esc(s.memory_share))`)
	}
}
