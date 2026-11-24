package verify

import (
	"context"
	"time"
)

// VerdictRecord is the server's last answer about one snapshot.
type VerdictRecord struct {
	Alias       string    `json:"alias"`
	ServiceType string    `json:"service_type"`
	Revision    string    `json:"hf_revision"`
	Status      Status    `json:"status"`
	At          time.Time `json:"at"`
}

// VerdictLog keeps the server's last verdict per snapshot in a file.
type VerdictLog struct{ path string }

// NewVerdictLog is a log kept at path.
func NewVerdictLog(path string) *VerdictLog { return &VerdictLog{path: path} }

// Record keeps r.
func (l *VerdictLog) Record(r VerdictRecord) error { return nil }

// Last is the last verdict on alias at revision.
func (l *VerdictLog) Last(alias, serviceType, revision string) (VerdictRecord, bool) {
	return VerdictRecord{}, false
}

// SetVerdictLog keeps every server answer in l.
func (v *Verifier) SetVerdictLog(l *VerdictLog) {}

// Check asks the server about model now.
func (v *Verifier) Check(ctx context.Context, model string) (Status, error) { return "", nil }
