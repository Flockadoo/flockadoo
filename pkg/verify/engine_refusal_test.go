package verify

import (
	"context"
	"strings"
	"testing"
)

// An engine below the supported minimum (vLLM 0.30.0) serves nothing through
// Inferoute: while the refusal is set, every request is refused with the
// reason, even for a model the node verified, and no call to the node is made.
func TestVerdictRefusesWhileTheEngineIsUnsupported(t *testing.T) {
	w := newWorld(t, "m")
	w.nodeSays("verified")
	w.v.Refresh(context.Background(), "m")
	expectVerdict(t, w, "m", Serve, "verified, engine supported")

	calls := w.verifyCalls()
	w.v.SetEngineRefusal("vLLM 0.29.2 is older than 0.30.0")
	got, why := w.v.Verdict("m")
	if got != Refuse || !strings.Contains(why, "vLLM 0.29.2") {
		t.Fatalf("verdict = %v (%s), want a refusal with the reason", got, why)
	}
	if w.verifyCalls() != calls {
		t.Fatal("the verdict called the node")
	}
}

func TestVerdictServesAgainOnceTheRefusalIsCleared(t *testing.T) {
	w := newWorld(t, "m")
	w.nodeSays("verified")
	w.v.Refresh(context.Background(), "m")
	w.v.SetEngineRefusal("vLLM 0.29.2 is older than 0.30.0")
	expectVerdict(t, w, "m", Refuse, "engine below the minimum")
	w.v.SetEngineRefusal("")
	expectVerdict(t, w, "m", Serve, "engine upgraded")
}
