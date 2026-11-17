// Package relay is the inference relay (plan item B2, section 15.3). It
// holds the envelope and the outcome; the handler moves here with item C3.
package relay

import (
	"encoding/json"
	"errors"
)

// Envelope is the only part of a request body the client reads.
type Envelope struct {
	Model  string
	Stream bool
}

// ParseEnvelope reads model and stream from the top level of a JSON object.
// Nothing nested counts (finding STR-2), and a body that is not a JSON object,
// or whose model or stream has the wrong type, is an error.
func ParseEnvelope(body []byte) (Envelope, error) {
	var e struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		return Envelope{}, errors.New("the request body is not a JSON object with a string model and a boolean stream")
	}
	return Envelope{Model: e.Model, Stream: e.Stream}, nil
}
