package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Flockadoo/flockadoo/pkg/logger"
	"go.uber.org/zap"
)

// VerdictRecord is the server's last answer about one snapshot (model
// revisions C2). It is what `flockadoo model list` shows; it is never used to
// serve, which only the verifier's own state decides.
type VerdictRecord struct {
	Alias       string    `json:"alias"`
	ServiceType string    `json:"service_type"`
	Revision    string    `json:"hf_revision"`
	Status      Status    `json:"status"`
	At          time.Time `json:"at"`
}

// VerdictLog keeps the server's last verdict per snapshot in a small JSON
// file. It holds answers only: a failure to ask the server is not a verdict.
type VerdictLog struct {
	path string
	mu   sync.Mutex
}

// NewVerdictLog is a log kept at path.
func NewVerdictLog(path string) *VerdictLog { return &VerdictLog{path: path} }

type verdictFile struct {
	Verdicts []VerdictRecord `json:"verdicts"`
}

func (l *VerdictLog) read() (verdictFile, error) {
	var f verdictFile
	data, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return verdictFile{}, fmt.Errorf("%s: %w", l.path, err)
	}
	return f, nil
}

// Record keeps r in place of any earlier verdict on the same snapshot. The
// file is replaced whole, so a reader never sees half of it.
func (l *VerdictLog) Record(r VerdictRecord) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.read()
	if err != nil {
		f = verdictFile{} // an unreadable log is started again; it only informs
	}
	kept := f.Verdicts[:0]
	for _, old := range f.Verdicts {
		if old.Alias != r.Alias || old.ServiceType != r.ServiceType || old.Revision != r.Revision {
			kept = append(kept, old)
		}
	}
	f.Verdicts = append(kept, r)
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".model-verdicts-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), l.path)
}

// Last is the last verdict on alias at revision for serviceType.
func (l *VerdictLog) Last(alias, serviceType, revision string) (VerdictRecord, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.read()
	if err != nil {
		return VerdictRecord{}, false
	}
	for _, r := range f.Verdicts {
		if r.Alias == alias && r.ServiceType == serviceType && r.Revision == revision {
			return r, true
		}
	}
	return VerdictRecord{}, false
}

// SetVerdictLog keeps every answer the server gives in l.
func (v *Verifier) SetVerdictLog(l *VerdictLog) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.verdicts = l
}

// Check measures model now and asks the server (`flockadoo model verify`). It
// returns the server's verdict, or why there is none.
func (v *Verifier) Check(ctx context.Context, model string) (Status, error) {
	if err := v.refresh(ctx, model); err != nil {
		return "", err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.states[model].status, nil
}

// record keeps the server's answer about the snapshot at root.
func (v *Verifier) record(model, root string, status Status) {
	v.mu.Lock()
	l := v.verdicts
	v.mu.Unlock()
	if l == nil {
		return
	}
	rev := v.servedRevision()
	if filepath.Base(filepath.Dir(root)) == "snapshots" {
		rev = filepath.Base(root)
	}
	if err := l.Record(VerdictRecord{Alias: model, ServiceType: v.serviceType, Revision: rev, Status: status, At: time.Now()}); err != nil {
		logger.Warn("could not keep the model verdict", zap.String("model", model), zap.Error(err))
	}
}
