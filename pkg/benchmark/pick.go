package benchmark

import "time"

const (
	minDecodeFraction = 0.5
	minDecodeTPSFloor = 15.0
	maxP95TTFT        = 3 * time.Second
	minThroughputGain = 1.10
	// maxEmbeddingLatency bounds the slowest embedding request at a level that
	// passes: the same bound as a chat stream's first token.
	maxEmbeddingLatency = 3 * time.Second
)

// LevelResult is one concurrency step of a benchmark.
type LevelResult struct {
	N            int           `json:"n"`
	Errors       int           `json:"errors"`
	Preemptions  int           `json:"preemptions"`
	P95TTFT      time.Duration `json:"p95_ttft"`
	MinDecodeTPS float64       `json:"min_decode_tps"`
	AggregateTPS float64       `json:"aggregate_tps"`
	// P95Latency and InputsPerSecond are an embedding level's: its slowest
	// request, and the inputs embedded per second across all of them.
	P95Latency      time.Duration `json:"p95_latency,omitempty"`
	InputsPerSecond float64       `json:"inputs_per_s,omitempty"`
}

// pickEmbedding is Pick for an embedding model. It keeps the highest level
// where every request still succeeds, nothing is evicted, the slowest request
// takes at most maxEmbeddingLatency, and total throughput is still rising by
// minThroughputGain. The thresholds are policy. 0 means no level passed.
func pickEmbedding(levels []LevelResult) int {
	best := 0
	for i, l := range levels {
		if l.Errors > 0 || l.Preemptions > 0 || l.P95Latency > maxEmbeddingLatency {
			break
		}
		if i > 0 && l.InputsPerSecond < levels[i-1].InputsPerSecond*minThroughputGain {
			break
		}
		best = l.N
	}
	return best
}

// Pick keeps the highest level where every stream still succeeds, nothing is
// evicted, the first token arrives quickly, each stream keeps half its solo
// speed, and total output is still rising. The thresholds are policy, not
// measurements.
//
// 0 means no tried level passed. The watcher uses that to stop climbing:
// a floor of 1 made Pick(failed n=1)==1, so 16 concurrent 2000-token
// streams still ran after the first level already lost.
func Pick(levels []LevelResult) int {
	if len(levels) == 0 {
		return 0
	}
	solo := levels[0].MinDecodeTPS
	best := 0
	for i, l := range levels {
		if l.Errors > 0 || l.Preemptions > 0 || l.P95TTFT > maxP95TTFT {
			break
		}
		floor := solo * minDecodeFraction
		if floor < minDecodeTPSFloor {
			floor = minDecodeTPSFloor
		}
		if l.MinDecodeTPS < floor {
			break
		}
		if i > 0 && l.AggregateTPS < levels[i-1].AggregateTPS*minThroughputGain {
			break
		}
		best = l.N
	}
	return best
}
