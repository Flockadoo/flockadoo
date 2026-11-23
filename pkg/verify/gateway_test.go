package verify

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Flockadoo/flockadoo/internal/testenv/fakeplatform"
)

// The server's verify-model handler answers 200 with a verdict, 400 for a
// body it cannot read, 401 for a refused key, 426 for a refused client and
// 500 when it cannot judge. It never answers 404, 408 or 429 itself: those
// come from the gateway in front of it (Traefik has no route while the
// service restarts) or from a limiter, and mean the server was not asked.
// Taking one as an answer stored "unverified", which the health report sent
// on and the server marked failed, until the next refresh (2026-11-23, after
// a dev stack rebuild). Invariant I14: an outage is never a verdict.

// gatewaySays makes every verify-model call get code without reaching the
// handler.
func (w *world) gatewaySays(code int) {
	w.plat.Set(http.MethodPost, "/api/provider/verify-model", fakeplatform.JSON(code, map[string]string{"error": http.StatusText(code)}))
}

func TestAGatewayAnswerIsAnOutageNotAVerdict(t *testing.T) {
	for _, code := range []int{
		http.StatusNotFound,           // Traefik while the service restarts
		http.StatusRequestTimeout,     // a proxy gave up waiting
		http.StatusTooManyRequests,    // a limiter, not the handler
		http.StatusBadGateway,         // control: already an outage
		http.StatusServiceUnavailable, // control
	} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			w := newWorld(t, "m")

			// A client starting while the server restarts.
			w.gatewaySays(code)
			w.v.Refresh(context.Background(), "m")
			expectVerdict(t, w, "m", Unavailable, "never verified, the gateway answered")
			if st := w.v.Status("m"); st != "" {
				t.Fatalf("never verified, the gateway answered %d: the health report would say %q, want nothing", code, st)
			}

			// Verified, then the server restarts during a refresh.
			w.nodeSays("verified")
			w.v.Refresh(context.Background(), "m")
			w.gatewaySays(code)
			time.Sleep(2 * refreshAfter)
			w.v.Refresh(context.Background(), "m")
			expectVerdict(t, w, "m", Serve, "verified, then the gateway answered inside the grace")
			if st := w.v.Status("m"); st != StatusVerified {
				t.Fatalf("verified, then the gateway answered %d: the health report would say %q, want %q", code, st, StatusVerified)
			}
		})
	}
}

// Negative case: an answer from the handler that refuses the call is not an
// outage. It blocks at once, with no grace on the last verified answer.
func TestARefusalFromTheServerIsNotAnOutage(t *testing.T) {
	for _, code := range []int{
		http.StatusBadRequest,      // the handler could not read the body
		http.StatusUnauthorized,    // the key was refused
		http.StatusUpgradeRequired, // the client version was refused
	} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			w := newWorld(t, "m")
			w.nodeSays("verified")
			w.v.Refresh(context.Background(), "m")
			time.Sleep(2 * refreshAfter)
			w.gatewaySays(code)
			w.v.Refresh(context.Background(), "m")
			if got, why := w.v.Verdict("m"); got != Refuse {
				t.Fatalf("after a %d the verdict is %v (%s), want Refuse", code, got, why)
			}
		})
	}
}
