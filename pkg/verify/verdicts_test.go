package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Flockadoo/flockadoo/internal/testenv/fakeplatform"
)

// Model revisions plan, item C2 (second half). The client keeps the server's
// last verdict for each snapshot in a small state file, written whenever the
// server answers (from the health report's refreshes or `flockadoo model
// verify`), so `flockadoo model list` can show it without hashing anything.
// A failure to ask the server records nothing: the file holds answers only.

const servedCommit = "aa8e72537993ba99e69dfaafa59ed015b17504d1"

// snapshotWorld is org/m at servedCommit in a hub cache, with a tokenizer file.
func snapshotWorld(t *testing.T, tokenizer string) (hub string) {
	t.Helper()
	hub = t.TempDir()
	snap := filepath.Join(hub, HFRepoToCacheDir("org/m"), "snapshots", servedCommit)
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"config.json":           []byte(`{"model_type":"m"}`),
		"tokenizer_config.json": []byte(tokenizer),
		"model.safetensors":     safetensors(`{"meta":{},"tensors":{}}`, []byte("w")),
	} {
		if err := os.WriteFile(filepath.Join(snap, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return hub
}

func catalogFor(t *testing.T, url string) *Catalog {
	t.Helper()
	cat := NewCatalog(url, "vllm")
	if err := cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return cat
}

var orgM = map[string]any{"object": "list", "data": []map[string]any{
	{"id": "e1", "alias": "org/m", "service_type": "vllm", "hf_repo": "org/m", "hf_ref": "main", "is_active": true},
}}

func TestTheServersVerdictIsKeptForTheSnapshotItJudged(t *testing.T) {
	hub := snapshotWorld(t, `{"a":1}`)
	plat := fakeplatform.New(t, "k")
	plat.Set(http.MethodGet, "/api/models/approved-builds", fakeplatform.JSON(http.StatusOK, orgM))
	plat.Set(http.MethodPost, "/api/provider/verify-model", fakeplatform.JSON(http.StatusOK, map[string]string{"verification_status": "verified"}))
	path := filepath.Join(t.TempDir(), "model-verdicts.json")

	v := NewVerifier(catalogFor(t, plat.URL), NewServerClient(plat.URL, "k"), "vllm", hub, "")
	v.SetRevision(servedCommit)
	v.SetVerdictLog(NewVerdictLog(path))
	before := time.Now()
	status, err := v.Check(context.Background(), "org/m")
	if err != nil || status != StatusVerified {
		t.Fatalf("Check = %q, %v; want verified", status, err)
	}

	// A new log on the same file: the verdict outlives the process.
	rec, ok := NewVerdictLog(path).Last("org/m", "vllm", servedCommit)
	if !ok || rec.Status != StatusVerified || rec.At.Before(before.Add(-time.Second)) {
		t.Errorf("Last = %+v, %v; want verified, recorded now", rec, ok)
	}
	if _, ok := NewVerdictLog(path).Last("org/m", "vllm", "1111111111111111111111111111111111111111"); ok {
		t.Errorf("a verdict for a commit never judged")
	}
}

// An outage is not a verdict: nothing is written, and Check says why.
func TestAFailureToAskTheServerRecordsNothing(t *testing.T) {
	hub := snapshotWorld(t, `{"a":1}`)
	plat := fakeplatform.New(t, "k")
	plat.Set(http.MethodGet, "/api/models/approved-builds", fakeplatform.JSON(http.StatusOK, orgM))
	plat.Set(http.MethodPost, "/api/provider/verify-model", fakeplatform.JSON(http.StatusBadGateway, map[string]string{"error": "upstream unavailable"}))
	path := filepath.Join(t.TempDir(), "model-verdicts.json")

	v := NewVerifier(catalogFor(t, plat.URL), NewServerClient(plat.URL, "k"), "vllm", hub, "")
	v.SetRevision(servedCommit)
	v.SetVerdictLog(NewVerdictLog(path))
	if _, err := v.Check(context.Background(), "org/m"); err == nil {
		t.Errorf("Check during an outage: no error")
	}
	if _, ok := NewVerdictLog(path).Last("org/m", "vllm", servedCommit); ok {
		t.Errorf("an outage was recorded as a verdict")
	}
}

// The verdict is the server's: one changed tokenizer file is measured, sent,
// and judged "does not match" (failed) by a server that knows the approved hash.
func TestAChangedTokenizerFileIsJudgedFailed(t *testing.T) {
	approved := `{"a":1}`
	sum := sha256.Sum256([]byte(approved))
	want := hex.EncodeToString(sum[:])
	judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(orgM)
			return
		}
		var req verifyModelRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		status := "failed"
		for _, f := range req.Files {
			if f.Name == "tokenizer_config.json" && f.Hash == want {
				status = "verified"
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"verification_status": status})
	}))
	t.Cleanup(judge.Close)

	for tokenizer, wantStatus := range map[string]Status{approved: StatusVerified, `{"a":2}`: StatusFailed} {
		hub := snapshotWorld(t, tokenizer)
		v := NewVerifier(catalogFor(t, judge.URL), NewServerClient(judge.URL, "k"), "vllm", hub, "")
		v.SetRevision(servedCommit)
		v.SetVerdictLog(NewVerdictLog(filepath.Join(t.TempDir(), "v.json")))
		if got, err := v.Check(context.Background(), "org/m"); err != nil || got != wantStatus {
			t.Errorf("tokenizer %s: Check = %q, %v; want %q", tokenizer, got, err, wantStatus)
		}
	}
}
