package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Flockadoo/flockadoo/internal/admission"
	"github.com/Flockadoo/flockadoo/internal/relay"
	"github.com/Flockadoo/flockadoo/pkg/usermsg"
	"github.com/Flockadoo/flockadoo/pkg/verify"
)

const streamWriteDeadlineGrace = 5 * time.Second

// maxRequestBody is the largest request body the relay reads: twice the
// node's own limit (8 MiB), so the server's refusal comes first (finding NET-5).
const maxRequestBody = 16 << 20

// maxStreamDuration is the longest stream deadline the platform's header can
// set. A larger value is capped: unchecked, 10^13 ms and more overflowed the
// duration and put the deadline in the past (found by FuzzStreamWriteDeadline).
const maxStreamDuration = 24 * time.Hour

// streamWriteDeadline is when a stream must stop being written. The platform
// sends how long the stream may run (X-Flockadoo-Timeout-Ms), and the deadline
// is counted from when the request arrived here. It is a duration and not a
// time of day because the platform's clock and this machine's are not the same.
func streamWriteDeadline(header string, arrived time.Time) (time.Time, bool) {
	ms, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}, false
	}
	limit := maxStreamDuration
	if ms < limit.Milliseconds() {
		limit = time.Duration(ms) * time.Millisecond
	}
	return arrived.Add(limit + streamWriteDeadlineGrace), true
}

// handleHealth handles the /api/health endpoint
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	report, err := s.healthReporter.GetHealthReport(r.Context())
	if err != nil {
		s.logError(fmt.Sprintf("Failed to get health report: %v", err))
		http.Error(w, usermsg.HTTP(err, s.config.PlatformType()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(report)
}

// handleBusy handles the /api/busy endpoint
func (s *Server) handleBusy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(BusyResponse{Busy: s.slotsBusy()})
}

// writeJSONError answers a refused request with the server's error shape.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: msg})
}

// handleChatCompletions handles the /v1/chat/completions endpoint
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	s.handleInference(w, r, "/v1/chat/completions")
}

// handleCompletions handles the /v1/completions endpoint
func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
	s.handleInference(w, r, "/v1/completions")
}

// handleEmbeddings handles the /v1/embeddings endpoint
func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	s.handleInference(w, r, "/v1/embeddings")
}

// defaultEngineIdleLimit ends a streamed engine answer that sends nothing for
// this long. It is longer than the server's own limits on a stream (120 s to the
// first token, 60 s between tokens), so the server's verdict comes first; this
// one bounds what the server does not (finding STR-1).
const defaultEngineIdleLimit = 180 * time.Second

// errStalled is why the idle watchdog cancels an engine call.
var errStalled = errors.New("the engine sent nothing for the idle limit")

// result is how one request ended: the status sent, the one outcome, and
// whether the response must be aborted because it failed after it started.
type result struct {
	status  int
	outcome relay.Outcome
	abort   bool
}

// handleInference runs one request from arrival to its one recorded outcome
// (plan item B2, section 15.3). A failure after the response started aborts
// the stream, so the gateway never mistakes a broken answer for a complete
// one (invariants I5 and I6).
func (s *Server) handleInference(w http.ResponseWriter, r *http.Request, llmPath string) {
	start := time.Now()
	res := result{status: http.StatusInternalServerError, outcome: relay.Panicked, abort: true}
	defer func() {
		if p := recover(); p != nil {
			if err, ok := p.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
				s.logError(fmt.Sprintf("relay panic: %v", p))
			}
		}
		s.logRequest(r.Method, r.URL.Path, res.status, res.outcome, start)
		if res.abort {
			panic(http.ErrAbortHandler)
		}
	}()
	res = s.relayRequest(w, r, llmPath, start)
}

func (s *Server) relayRequest(w http.ResponseWriter, r *http.Request, llmPath string, start time.Time) result {
	refuse := func(status int, outcome relay.Outcome, msg string) result {
		writeJSONError(w, status, msg)
		return result{status: status, outcome: outcome}
	}

	// The request arrived on the tunnel this client dialled with its key, so
	// it comes from Flockadoo. It is not checked with the server again
	// (decision DC1): serving a request makes no call to the server.

	// A benchmark owns the engine: refuse before reading the body. The gate
	// refuses it again, atomically, when the slot is taken.
	if !s.admit().Snapshot().Accepting {
		return refuse(http.StatusServiceUnavailable, relay.RefusedBusy, "GPU is busy")
	}

	// A matching X-Session-Key means this conversation was served here
	// recently. Those turns queue for a slot instead of being rejected.
	sessionKey := r.Header.Get("X-Session-Key")
	sameSession := sessionKey != "" && s.isActiveSession(sessionKey)
	if !sameSession && s.slotsBusy() {
		return refuse(http.StatusServiceUnavailable, relay.RefusedBusy, "GPU is busy")
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		if mbe := (*http.MaxBytesError)(nil); errors.As(err, &mbe) {
			return refuse(http.StatusRequestEntityTooLarge, relay.RefusedTooLarge, fmt.Sprintf("request body is over %d bytes", maxRequestBody))
		}
		return refuse(http.StatusBadRequest, relay.RefusedBadRequest, "could not read the request body")
	}
	env, err := relay.ParseEnvelope(body)
	if err != nil {
		return refuse(http.StatusBadRequest, relay.RefusedBadRequest, err.Error())
	}
	if s.config != nil && s.config.Provider.Model != "" && env.Model != "" && env.Model != s.config.Provider.Model {
		return refuse(http.StatusServiceUnavailable, relay.RefusedModel, "model_not_served")
	}
	switch verdict, why := s.verifyModel(env.Model); verdict {
	case verify.Serve:
	case verify.Refuse:
		return refuse(http.StatusForbidden, relay.RefusedUnverified, "model is not verified: "+why)
	case verify.Unavailable:
		return refuse(http.StatusServiceUnavailable, relay.RefusedVerificationUnavailable, why)
	}

	var slot *admission.Slot
	if sameSession {
		// Returning conversations wait for a free slot, in arrival order and
		// up to the queue limit. Parallel calls that share a key are admitted
		// together, up to the slot limit.
		slot, _ = s.admit().Acquire(r.Context(), s.sessionQueueWait, s.queueLimit())
	} else {
		slot, _ = s.admit().TryAcquire()
	}
	if slot == nil {
		return refuse(http.StatusServiceUnavailable, relay.RefusedBusy, "GPU is busy")
	}
	defer slot.Release()
	s.setActiveSession(sessionKey)

	// One deadline for the whole request, set before the engine is asked
	// (finding STR-4): the platform's time limit for a stream, counted from
	// arrival.
	ctx := r.Context()
	if when, ok := streamWriteDeadline(r.Header.Get("X-Flockadoo-Timeout-Ms"), start); ok {
		_ = http.NewResponseController(w).SetWriteDeadline(when)
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, when)
		defer cancel()
	}
	engineCtx, cancelEngine := context.WithCancelCause(ctx)
	defer cancelEngine(nil)
	ended := func(status int, fallback relay.Outcome, abort bool) result {
		switch {
		case errors.Is(context.Cause(engineCtx), errStalled):
			return result{status: status, outcome: relay.Stalled, abort: abort}
		case r.Context().Err() != nil:
			return result{status: status, outcome: relay.ConsumerGone, abort: abort}
		case ctx.Err() != nil:
			return result{status: status, outcome: relay.DeadlineExceeded, abort: abort}
		}
		return result{status: status, outcome: fallback, abort: abort}
	}

	idle := s.engineIdleLimit
	if idle <= 0 {
		idle = defaultEngineIdleLimit
	}
	// The watchdog bounds a stream only (finding STR-1): its engine client
	// has no timeout. A plain answer sends nothing until it is generated, so
	// its silence is normal; the engine client's timeout (llm_timeout_seconds)
	// and the server's own limit bound it.
	var watchdog *time.Timer
	if env.Stream {
		watchdog = time.AfterFunc(idle, func() { cancelEngine(errStalled) })
		defer watchdog.Stop()
	}

	llmResp, err := s.llmClient.ForwardRequest(engineCtx, llmPath, body, env.Stream)
	if err != nil {
		s.logError(fmt.Sprintf("Failed to forward request to LLM provider: %v", err))
		http.Error(w, usermsg.HTTP(err, s.config.PlatformType()), http.StatusBadGateway)
		s.suspectEngine()
		return ended(http.StatusBadGateway, relay.EngineUnreachable, false)
	}
	defer func() { _ = llmResp.Body.Close() }()

	if ct := llmResp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	status := llmResp.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		if watchdog != nil {
			watchdog.Reset(idle)
		}
		n, readErr := llmResp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return ended(status, relay.ConsumerGone, true)
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			return result{status: status, outcome: relay.Completed}
		}
		if readErr != nil {
			s.logError(fmt.Sprintf("Failed to read LLM response: %v", readErr))
			return ended(status, relay.EngineFailed, true)
		}
	}
}
