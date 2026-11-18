package llm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// EngineCapacity is what the local engine's /metrics page says.
// Nil fields mean the engine did not expose that metric.
type EngineCapacity struct {
	Waiting      *int32
	KVCacheUsage *float64
	Preemptions  *int64
}

// EngineCapacity GETs /metrics and parses the gauges Inferoute routes on.
func (c *VLLMClient) EngineCapacity(ctx context.Context) (EngineCapacity, error) {
	url := fmt.Sprintf("%s/metrics", strings.TrimRight(c.baseURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return EngineCapacity{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return EngineCapacity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return EngineCapacity{}, fmt.Errorf("metrics: %w", wrapHTTPStatusErr(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return EngineCapacity{}, err
	}
	return ParsePrometheusMetrics(string(body)), nil
}

// ParsePrometheusMetrics reads vLLM and llama.cpp gauges. The first good
// sample of a name wins. A missing name stays nil, which is not the same as
// zero; so does a sample that is not finite or is out of range for its gauge
// (a KV cache usage outside 0 to 1, a negative count), which the capacity
// report would otherwise carry to the node, or fail to encode at all (NaN).
func ParsePrometheusMetrics(text string) EngineCapacity {
	var out EngineCapacity
	var kvFallback *float64
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := promSample(line)
		if !ok {
			continue
		}
		switch name {
		case "vllm:num_requests_waiting", "llamacpp:requests_deferred":
			if out.Waiting == nil && count(value, math.MaxInt32) {
				v := int32(value)
				out.Waiting = &v
			}
		case "vllm:kv_cache_usage_perc", "llamacpp:kv_cache_usage_ratio":
			if v := normalizeKV(value); out.KVCacheUsage == nil && ratio(v) {
				out.KVCacheUsage = &v
			}
		case "vllm:gpu_cache_usage_perc":
			if v := normalizeKV(value); kvFallback == nil && ratio(v) {
				kvFallback = &v
			}
		case "vllm:num_preemptions_total":
			if out.Preemptions == nil && count(value, 1<<62) {
				v := int64(value)
				out.Preemptions = &v
			}
		}
	}
	if out.KVCacheUsage == nil {
		out.KVCacheUsage = kvFallback
	}
	return out
}

func promSample(line string) (string, float64, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", 0, false
	}
	name := fields[0]
	if i := strings.IndexByte(name, '{'); i >= 0 {
		name = name[:i]
	}
	value, err := strconv.ParseFloat(fields[len(fields)-1], 64)
	if err != nil {
		return "", 0, false
	}
	return name, value, true
}

// count reports whether v is a whole-number gauge the client can carry: not
// NaN, not negative, and at most limit.
func count(v float64, limit float64) bool {
	return v >= 0 && v <= limit
}

// ratio reports whether v is a usable 0 to 1 ratio (NaN fails both tests).
func ratio(v float64) bool {
	return v >= 0 && v <= 1
}

// Older vLLM reported cache usage as 0-100. Current gauges are 0-1.
func normalizeKV(v float64) float64 {
	if v > 1 {
		return v / 100
	}
	return v
}
