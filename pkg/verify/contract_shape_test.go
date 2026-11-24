package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestVerifyRequestShape(t *testing.T) {
	got, err := json.Marshal(verifyModelRequest{
		Alias: "qwen2.5-7b-instruct", ServiceType: "vllm", SizeBytes: 100,
		Files: []FileMeasurement{{Name: "model.safetensors", Hash: "abc", HashMethod: "sha256", Size: 100}},
		// The server reads stale (node: pkg/api/provider/models.go, Stale); the
		// client sends it when a model's weights changed since its last
		// measurement. The golden lacked it until the fake platform's check
		// caught it (plan item B3).
		Stale: true,
		// The commit measured (model revisions C5), a hint the server tries first.
		HFRevision: "aa8e72537993ba99e69dfaafa59ed015b17504d1",
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "contract", "testdata", "verify-model.request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("verify payload drifted\ngot  %s\nwant %s", got, want)
	}
}
