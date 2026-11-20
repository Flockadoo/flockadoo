package server

import (
	"net/http"
	"testing"

	"github.com/Flockadoo/flockadoo/pkg/verify"
)

type fixedVerdict struct {
	v   verify.Verdict
	why string
}

func (f fixedVerdict) Verdict(string) (verify.Verdict, string) { return f.v, f.why }

// Plan item B3, invariant I3: a request refused by verification (403), or
// made while verification is unavailable (503), never reaches the engine.
func TestAVerificationVerdictIsAppliedBeforeTheEngine(t *testing.T) {
	cases := []struct {
		verdict verify.Verdict
		status  int
		outcome string
	}{
		{verify.Refuse, http.StatusForbidden, "refused_unverified"},
		{verify.Unavailable, http.StatusServiceUnavailable, "refused_verification_unavailable"},
		{verify.Serve, http.StatusOK, "completed"},
	}
	for _, tc := range cases {
		r := newRig(t, 1)
		r.s.verifier = fixedVerdict{v: tc.verdict, why: "test"}
		before, _ := r.outcomes()
		status, body := r.call(t, streamBody, nil)
		if status != tc.status {
			t.Fatalf("verdict %v: status %d (%s), want %d", tc.verdict, status, body, tc.status)
		}
		if got := r.awaitOutcomes(t, before, 1); got[0] != tc.outcome {
			t.Fatalf("verdict %v: outcome %q, want %q", tc.verdict, got[0], tc.outcome)
		}
		if served := len(r.eng.Requests()); (tc.verdict == verify.Serve) != (served == 1) {
			t.Fatalf("verdict %v: the engine received %d requests", tc.verdict, served)
		}
	}
}
