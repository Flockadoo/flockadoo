package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/pkg/usermsg"
)

const streamWriteDeadlineGrace = 5 * time.Second

// maxStreamDuration is the longest stream deadline the platform's header can
// set. A larger value is capped: unchecked, 10^13 ms and more overflowed the
// duration and put the deadline in the past (found by FuzzStreamWriteDeadline).
const maxStreamDuration = 24 * time.Hour

// streamWriteDeadline is when a stream must stop being written. The platform
// sends how long the stream may run (X-Inferoute-Timeout-Ms), and the deadline
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

func (s *Server) modelNotServed(w http.ResponseWriter, r *http.Request, body []byte, startTime time.Time) bool {
	if s.config == nil || s.config.Provider.Model == "" {
		return false
	}
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Model == "" || payload.Model == s.config.Provider.Model {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "model_not_served"})
	s.logRequest(r.Method, r.URL.Path, http.StatusServiceUnavailable, startTime)
	return true
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
	json.NewEncoder(w).Encode(report)
}

// handleBusy handles the /api/busy endpoint
func (s *Server) handleBusy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(BusyResponse{Busy: s.slotsBusy()})
}

func (s *Server) writeBusy(w http.ResponseWriter, r *http.Request, startTime time.Time) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	json.NewEncoder(w).Encode(ErrorResponse{Error: "GPU is busy"})
	s.logRequest(r.Method, r.URL.Path, http.StatusServiceUnavailable, startTime)
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

func (s *Server) handleInference(w http.ResponseWriter, r *http.Request, llmPath string) {
	startTime := time.Now()

	// The request arrived on the tunnel this client dialled with its key, so
	// it comes from Inferoute. It is not checked with the node again
	// (decision DC1): serving a request makes no call to the node.

	// A benchmark owns the engine. Returning conversations do not skip this:
	// they skip the slot check below, and would otherwise land mid-benchmark.
	if s.benchmarking.Load() {
		s.writeBusy(w, r, startTime)
		return
	}

	// A matching X-Session-Key means this conversation was served here
	// recently. Those turns queue for a slot instead of being rejected.
	sessionKey := r.Header.Get("X-Session-Key")
	sameSession := sessionKey != "" && s.isActiveSession(sessionKey)

	if !sameSession && s.slotsBusy() {
		s.writeBusy(w, r, startTime)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logError(fmt.Sprintf("Failed to read request body: %v", err))
		http.Error(w, fmt.Sprintf("Failed to read request body: %v", err), http.StatusBadRequest)
		s.logRequest(r.Method, r.URL.Path, http.StatusBadRequest, startTime)
		return
	}

	if s.modelNotServed(w, r, body, startTime) {
		return
	}
	if err := s.verifyModelInRequest(r.Context(), body); err != nil {
		s.logError(fmt.Sprintf("Model verification failed: %v", err))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(ErrorResponse{Error: err.Error()})
		s.logRequest(r.Method, r.URL.Path, http.StatusForbidden, startTime)
		return
	}

	acquired := false
	if sameSession {
		// Returning conversations wait for a free slot. Parallel calls that
		// share a key are admitted together, up to the slot limit.
		acquired = s.acquireInferenceWait(r.Context(), s.sessionQueueWait)
	} else {
		acquired = s.tryAcquireInference()
	}
	if !acquired {
		s.writeBusy(w, r, startTime)
		return
	}
	defer s.releaseInference()
	// The benchmark may have closed the gate after the check at the top,
	// while this request read its body or queued for a slot (up to
	// sessionQueueWait). It now waits for in-flight work to finish, so give
	// the slot back instead of sending this request into its measurement.
	if s.benchmarking.Load() {
		s.writeBusy(w, r, startTime)
		return
	}
	s.setActiveSession(sessionKey)

	llmResp, err := s.forwardToLLM(r.Context(), llmPath, body)
	if err != nil {
		s.logError(fmt.Sprintf("Failed to forward request to LLM provider: %v", err))
		http.Error(w, usermsg.HTTP(err, s.config.PlatformType()), http.StatusBadGateway)
		s.logRequest(r.Method, r.URL.Path, http.StatusBadGateway, startTime)
		return
	}
	defer llmResp.Body.Close()

	if ct := llmResp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	status := llmResp.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	if when, ok := streamWriteDeadline(r.Header.Get("X-Inferoute-Timeout-Ms"), startTime); ok {
		if err := http.NewResponseController(w).SetWriteDeadline(when); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			s.logRequest(r.Method, r.URL.Path, http.StatusInternalServerError, startTime)
			return
		}
	}
	w.WriteHeader(status)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, readErr := llmResp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				s.logError(fmt.Sprintf("Failed to write LLM response: %v", writeErr))
				s.logRequest(r.Method, r.URL.Path, http.StatusOK, startTime)
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			s.logError(fmt.Sprintf("Failed to read LLM response: %v", readErr))
			s.logRequest(r.Method, r.URL.Path, http.StatusOK, startTime)
			return
		}
	}
	s.logRequest(r.Method, r.URL.Path, status, startTime)
}
