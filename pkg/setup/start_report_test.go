package setup

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/config"
)

// Found in R on peipei (2026-11-25): when the engine is not started (a model
// that does not fit, say), the reason went only to stderr, where the console
// cleared it at once. The start report now carries it for the log and the
// dashboard.

func TestTheStartReportSaysWhyTheEngineIsNotReady(t *testing.T) {
	var order []string
	useFit(t, &order, errDoesNotFit)
	var out bytes.Buffer
	report, err := EnsureOnStartAt(t.Context(), autoStartConfig(t), "", Streams{In: strings.NewReader(""), Out: &out, Err: &out})
	if !errors.Is(err, errDoesNotFit) {
		t.Fatalf("EnsureOnStartAt = %v, want the refusal", err)
	}
	if report.NotReady != errDoesNotFit.Error() {
		t.Errorf("report.NotReady = %q, want %q", report.NotReady, errDoesNotFit.Error())
	}
}

// Negative: an engine that is already serving leaves nothing to report.
func TestARunningEngineLeavesNoNotReadyReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"org/m"}]}`))
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{}
	cfg.Provider.Engine = "vllm"
	cfg.Provider.LLMURL = srv.URL

	var out bytes.Buffer
	report, err := EnsureOnStartAt(t.Context(), cfg, "", Streams{In: strings.NewReader(""), Out: &out, Err: &out})
	if err != nil {
		t.Fatalf("EnsureOnStartAt = %v", err)
	}
	if report.NotReady != "" {
		t.Errorf("report.NotReady = %q, want empty", report.NotReady)
	}
}
