package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sentnl/inferoute-node/inferoute-client/pkg/nodeapi"
)

// ServerClient calls provider-authenticated verification APIs.
type ServerClient struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewServerClient creates a client for POST /api/provider/verify-model.
func NewServerClient(baseURL, apiKey string) *ServerClient {
	return &ServerClient{
		baseURL: stringsTrimRightSlash(baseURL),
		apiKey:  apiKey,
		client:  nodeapi.NewHTTPClient(30 * time.Second),
	}
}

// VerifyModel submits measurements; the server returns verification status only.
func (s *ServerClient) VerifyModel(ctx context.Context, req verifyModelRequest) (verifyModelResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return verifyModelResponse{}, err
	}

	url := fmt.Sprintf("%s/api/provider/verify-model", s.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return verifyModelResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", s.apiKey))

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return verifyModelResponse{}, fmt.Errorf("verify model: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return verifyModelResponse{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return verifyModelResponse{}, &NodeStatusError{Code: resp.StatusCode, Body: string(respBody)}
	}

	var out verifyModelResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return verifyModelResponse{}, fmt.Errorf("decode verify model response: %w", err)
	}
	return out, nil
}

// NodeStatusError is a verify-model call the node answered with an error
// status.
type NodeStatusError struct {
	Code int
	Body string
}

func (e *NodeStatusError) Error() string {
	return fmt.Sprintf("verify model returned %d: %s", e.Code, e.Body)
}

// unavailable reports whether err means the node could not be asked: a
// timeout, a failed connection, a 5xx, or an answer that cannot be read.
// A 401 or 426 is not an outage: the key or the client version was refused,
// and no grace applies (plan item B3, detail 4).
func unavailable(err error) bool {
	var se *NodeStatusError
	if errors.As(err, &se) {
		return se.Code >= 500
	}
	return err != nil
}
