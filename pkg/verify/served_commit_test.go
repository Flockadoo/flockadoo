package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Flockadoo/flockadoo/internal/testenv/fakeplatform"
)

// Model revisions plan, item C5. The verifier measures the commit the engine
// serves (provider.hf_revision), not the snapshot refs/main points at, and
// names that commit to the server as a hint. Before C5 the two could differ:
// the engine ran one commit and the client measured another.
func TestTheVerifierMeasuresTheCommitTheEngineServes(t *testing.T) {
	const (
		oldSHA    = "1111111111111111111111111111111111111111"
		servedSHA = "aa8e72537993ba99e69dfaafa59ed015b17504d1"
	)
	hub := t.TempDir()
	repo := filepath.Join(hub, HFRepoToCacheDir("org/m"))
	served := []byte(`{"model_type":"served"}`)
	for sha, config := range map[string][]byte{oldSHA: []byte(`{"model_type":"old"}`), servedSHA: served} {
		snap := filepath.Join(repo, "snapshots", sha)
		if err := os.MkdirAll(snap, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(snap, "config.json"), config, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(snap, "model.safetensors"), safetensors(`{"meta":{},"tensors":{}}`, []byte("w")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "refs", "main"), []byte(oldSHA), 0o600); err != nil {
		t.Fatal(err)
	}

	plat := fakeplatform.New(t, "k")
	plat.Set(http.MethodGet, "/api/models/approved-builds", fakeplatform.JSON(http.StatusOK, map[string]any{"object": "list", "data": []map[string]any{
		{"id": "e1", "alias": "org/m", "service_type": "vllm", "hf_repo": "org/m", "hf_ref": "main", "is_active": true},
	}}))
	plat.Set(http.MethodPost, "/api/provider/verify-model", fakeplatform.JSON(http.StatusOK, map[string]string{"verification_status": "verified"}))
	cat := NewCatalog(plat.URL, "vllm")
	if err := cat.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(cat, NewServerClient(plat.URL, "k"), "vllm", hub, "")
	v.SetRevision(servedSHA)
	v.Refresh(context.Background(), "org/m")

	reqs := plat.Requests(http.MethodPost, "/api/provider/verify-model")
	if len(reqs) != 1 {
		t.Fatalf("%d verify-model requests, want 1", len(reqs))
	}
	var body verifyModelRequest
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.HFRevision != servedSHA {
		t.Errorf("hf_revision %q, want the served commit %s", body.HFRevision, servedSHA)
	}
	sum := sha256.Sum256(served)
	want := hex.EncodeToString(sum[:])
	got := ""
	for _, f := range body.Files {
		if f.Name == "config.json" {
			got = f.Hash
		}
	}
	if got != want {
		t.Errorf("config.json measured as %s, want the served commit's %s (refs/main points at another)", got, want)
	}
}
