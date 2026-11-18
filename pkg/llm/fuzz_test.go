package llm

import (
	"encoding/json"
	"math"
	"testing"
)

// Plan item Q3: the engine's /metrics page is text the client did not write.
// Whatever it says, every gauge kept is finite and in range, and the result
// encodes as JSON for the capacity report.
func FuzzParsePrometheusMetrics(f *testing.F) {
	f.Add("vllm:num_requests_waiting{model_name=\"m\"} 3\nvllm:kv_cache_usage_perc{model_name=\"m\"} 0.5\n")
	f.Add("llamacpp:requests_deferred 2\nllamacpp:kv_cache_usage_ratio 0.25\n")
	f.Add("# HELP x\nvllm:num_preemptions_total 1e309\nvllm:gpu_cache_usage_perc NaN\n")
	f.Fuzz(func(t *testing.T, text string) {
		got := ParsePrometheusMetrics(text)
		if kv := got.KVCacheUsage; kv != nil && (math.IsNaN(*kv) || *kv < 0 || *kv > 1) {
			t.Fatalf("KV cache usage %v is outside [0, 1]", *kv)
		}
		if got.Waiting != nil && *got.Waiting < 0 {
			t.Fatalf("waiting %d is negative", *got.Waiting)
		}
		if got.Preemptions != nil && *got.Preemptions < 0 {
			t.Fatalf("preemptions %d is negative", *got.Preemptions)
		}
		if _, err := json.Marshal(got); err != nil {
			t.Fatalf("the parsed gauges do not encode: %v", err)
		}
	})
}
