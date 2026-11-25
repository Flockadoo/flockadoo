package server

import (
	"strings"
	"testing"
)

// Engine start memory plan, found in R on peipei (2026-11-25): when the engine
// is not started (a model that does not fit, say), the reason went to stderr
// once and the console cleared it half a second later. The machine then showed
// a client with no engine and no reason. The reason now stays in an Engine not
// ready row in the console, the dashboard and the status API, until the engine
// serves a model.

const notReady = "deepseek-ai/DeepSeek-R1-Distill-Qwen-7B needs about 24.03 GiB to start at a 131,072-token context; " +
	"vLLM can use 14.62 GiB: a share of 0.12 of the 121.86 GiB of memory shared by the CPU and GPU, leaving room for the system " +
	"(21.18 GiB is free). Close other programs, or choose a smaller model"

func TestWhyTheEngineIsNotReadyIsInTheStatusAPIAndTheConsole(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	s.SetEngineNotReady(notReady)
	if got, _ := statusJSON(t, s)["engine_not_ready"].(string); got != notReady {
		t.Errorf("status engine_not_ready = %q, want %q", got, notReady)
	}
	console := ansi.ReplaceAllString(consoleOutput(t, s), "")
	if !strings.Contains(console, "Engine not ready") || !strings.Contains(console, notReady) {
		t.Errorf("the console has no Engine not ready row with the reason:\n%s", console)
	}
}

func TestTheDashboardDrawsWhyTheEngineIsNotReady(t *testing.T) {
	if page := dashboardPage(t); !strings.Contains(page, `row("Engine not ready", esc(s.engine_not_ready), "bad")`) {
		t.Error(`the dashboard has no row("Engine not ready", esc(s.engine_not_ready), "bad")`)
	}
}

// Negative: a start that went well shows no row.
func TestAStartThatWentWellShowsNoEngineNotReadyRow(t *testing.T) {
	s := newTestServer("http://unused", &fakeLLM{})
	if _, ok := statusJSON(t, s)["engine_not_ready"]; ok {
		t.Error("status has engine_not_ready after a start that went well")
	}
	if console := ansi.ReplaceAllString(consoleOutput(t, s), ""); strings.Contains(console, "Engine not ready") {
		t.Errorf("the console has an Engine not ready row after a start that went well:\n%s", console)
	}
}

// Negative: once the engine serves a model (the operator started it by hand,
// or a slow start finished), the reason from the start no longer applies and
// the row goes.
func TestTheRowGoesOnceTheEngineServesAModel(t *testing.T) {
	s := engineServer(t, &versionedEngine{version: "0.34.0"})
	s.SetEngineNotReady(notReady)
	if _, ok := statusJSON(t, s)["engine_not_ready"]; ok {
		t.Error("status has engine_not_ready while the engine serves a model")
	}
	if console := ansi.ReplaceAllString(consoleOutput(t, s), ""); strings.Contains(console, "Engine not ready") {
		t.Errorf("the console has an Engine not ready row while the engine serves a model:\n%s", console)
	}
}
