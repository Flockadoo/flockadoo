package llm

import "testing"

// Plan item Q3: the engine's /metrics page is text the client did not write.
func FuzzParsePrometheusMetrics(f *testing.F) {
	f.Add("vllm:num_requests_waiting{model_name=\"m\"} 3\nvllm:kv_cache_usage_perc{model_name=\"m\"} 0.5\n")
	f.Add("llamacpp:requests_deferred 2\nllamacpp:kv_cache_usage_ratio 0.25\n")
	f.Add("# HELP x\nvllm:num_preemptions_total 1e309\nvllm:gpu_cache_usage_perc NaN\n")
	f.Fuzz(func(t *testing.T, text string) {
		got := ParsePrometheusMetrics(text)
		if got.KVCacheUsage != nil && (*got.KVCacheUsage < 0 || *got.KVCacheUsage > 1) {
			t.Fatalf("KV cache usage %v is outside [0, 1]", *got.KVCacheUsage)
		}
	})
}
