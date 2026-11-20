package llm

import (
	"fmt"
	"testing"
)

func TestParsePrometheusMetricsVLLM(t *testing.T) {
	text := `
# HELP vllm:num_requests_waiting Number of requests waiting.
vllm:num_requests_waiting{model="Qwen"} 3
vllm:kv_cache_usage_perc{model="Qwen"} 0.42
vllm:num_preemptions_total{model="Qwen"} 7
`
	got := ParsePrometheusMetrics(text)
	if got.Waiting == nil || *got.Waiting != 3 {
		t.Fatalf("waiting = %v", got.Waiting)
	}
	if got.KVCacheUsage == nil || *got.KVCacheUsage != 0.42 {
		t.Fatalf("kv = %v", got.KVCacheUsage)
	}
	if got.Preemptions == nil || *got.Preemptions != 7 {
		t.Fatalf("preemptions = %v", got.Preemptions)
	}
}

func TestParsePrometheusMetricsFallsBackAndScalesPercent(t *testing.T) {
	text := "vllm:gpu_cache_usage_perc 80\n"
	got := ParsePrometheusMetrics(text)
	if got.KVCacheUsage == nil || *got.KVCacheUsage != 0.8 {
		t.Fatalf("kv = %v", got.KVCacheUsage)
	}
	if got.Waiting != nil {
		t.Fatal("missing waiting must stay nil, not zero")
	}
}

func TestParsePrometheusMetricsLlama(t *testing.T) {
	text := "llamacpp:requests_deferred 1\nllamacpp:kv_cache_usage_ratio 0.25\n"
	got := ParsePrometheusMetrics(text)
	if got.Waiting == nil || *got.Waiting != 1 || got.KVCacheUsage == nil || *got.KVCacheUsage != 0.25 {
		t.Fatalf("%+v", got)
	}
}

// Found by FuzzParsePrometheusMetrics (plan item Q3): a gauge the engine
// reports out of range, or as NaN or Inf, is left out, as if it were
// missing. Kept, a KV cache usage above 1 reached the server, and a NaN made
// the capacity report fail to encode, so it was dropped.
func TestAGaugeOutOfRangeIsLeftOut(t *testing.T) {
	cases := []string{
		"llamacpp:kv_cache_usage_ratio 101",
		"vllm:kv_cache_usage_perc NaN",
		"vllm:kv_cache_usage_perc -0.5",
		"vllm:gpu_cache_usage_perc +Inf",
		"vllm:num_requests_waiting NaN",
		"vllm:num_requests_waiting -3",
		"llamacpp:requests_deferred 1e20",
		"vllm:num_preemptions_total -1",
		"vllm:num_preemptions_total Inf",
	}
	for _, text := range cases {
		got := ParsePrometheusMetrics(text)
		if got.KVCacheUsage != nil || got.Waiting != nil || got.Preemptions != nil {
			t.Errorf("%q gave %s", text, describe(got))
		}
	}
}

// A good sample after a bad one of the same name still counts.
func TestAGoodSampleAfterABadOneCounts(t *testing.T) {
	got := ParsePrometheusMetrics("vllm:num_requests_waiting NaN\nvllm:num_requests_waiting 4\n")
	if got.Waiting == nil || *got.Waiting != 4 {
		t.Fatalf("waiting = %s", describe(got))
	}
}

func describe(c EngineCapacity) string {
	s := ""
	if c.Waiting != nil {
		s += fmt.Sprintf("waiting=%d ", *c.Waiting)
	}
	if c.KVCacheUsage != nil {
		s += fmt.Sprintf("kv=%v ", *c.KVCacheUsage)
	}
	if c.Preemptions != nil {
		s += fmt.Sprintf("preemptions=%d ", *c.Preemptions)
	}
	return "{" + s + "}"
}
