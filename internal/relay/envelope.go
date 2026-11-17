// Package relay is the inference relay (plan item B2, section 15.3). It
// starts with the envelope; the handler moves here with item C3.
package relay

// Envelope is the only part of a request body the client reads.
type Envelope struct {
	Model  string
	Stream bool
}

// ParseEnvelope is not written yet: plan item B2, tests first.
func ParseEnvelope(body []byte) (Envelope, error) {
	return Envelope{}, nil
}
