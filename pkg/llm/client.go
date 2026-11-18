package llm

import (
	"context"
	"net/http"
	"time"
)

// Model represents a model in the LLM API
type Model struct {
	ID                 string `json:"id"`
	Object             string `json:"object"`
	Created            int64  `json:"created"`
	OwnedBy            string `json:"owned_by"`
	SizeBytes          int64  `json:"size_bytes,omitempty"`
	WeightFingerprint  string `json:"weight_fingerprint,omitempty"`
	VerificationStatus string `json:"verification_status,omitempty"`
	MaxModelLen        int64  `json:"max_model_len,omitempty"`
}

// ListModelsResponse represents the response from the LLM API for listing models
type ListModelsResponse struct {
	Object string  `json:"object"`
	Models []Model `json:"data"`
}

// Client interface defines the methods that any LLM client must implement
type Client interface {
	// ListModels lists all available models
	ListModels(ctx context.Context) (*ListModelsResponse, error)

	// ForwardRequest forwards a raw request to the LLM API. stream says
	// whether the answer is a stream (read from the request's envelope).
	// The caller must close the response body. A non-200 is returned with its status.
	ForwardRequest(ctx context.Context, path string, body []byte, stream bool) (*http.Response, error)
}

// DefaultTimeout is used when no explicit LLM timeout is configured. Aligned
// with the orchestrator's sticky inference timeout so queued same-session
// turns can finish decoding.
const DefaultTimeout = 120 * time.Second

// NewClient creates an OpenAI-compatible LLM client. vLLM, vLLM Metal, and
// llama.cpp all use it. timeout <= 0 falls back to DefaultTimeout.
func NewClient(providerType string, baseURL string, timeout time.Duration) Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return NewVLLMClient(baseURL, timeout)
}
