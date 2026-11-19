package benchmark

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	outputTokens = 256
	promptWords  = 2000
)

// Levels tried in order. The first failure stops the run.
var Levels = []int{1, 2, 4, 8, 16}

// RunLevel fires n concurrent streaming chats at the local engine and reports
// how the slowest stream behaved.
func RunLevel(ctx context.Context, client *http.Client, baseURL, model string, n int) LevelResult {
	type one struct {
		ttft   time.Duration
		decode float64
		tokens int
		err    error
	}
	out := make([]one, n)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = streamOnce(ctx, client, baseURL, model)
		}(i)
	}
	wg.Wait()
	wall := time.Since(start)
	res := LevelResult{N: n}
	var total int
	for _, o := range out {
		if o.err != nil {
			res.Errors++
			continue
		}
		if o.ttft > res.P95TTFT {
			res.P95TTFT = o.ttft
		}
		if res.MinDecodeTPS == 0 || o.decode < res.MinDecodeTPS {
			res.MinDecodeTPS = o.decode
		}
		total += o.tokens
	}
	if wall > 0 {
		res.AggregateTPS = float64(total) / wall.Seconds()
	}
	return res
}

func streamOnce(ctx context.Context, client *http.Client, baseURL, model string) (one struct {
	ttft   time.Duration
	decode float64
	tokens int
	err    error
}) {
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{{
			"role": "user", "content": prompt(),
		}},
		"max_tokens":  outputTokens,
		"temperature": 0,
		"stream":      true,
		"ignore_eos":  true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		one.err = err
		return
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		one.err = err
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		one.err = fmt.Errorf("status %d", resp.StatusCode)
		return
	}
	sc := bufio.NewScanner(resp.Body)
	var first time.Time
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		if !strings.Contains(data, `"content"`) {
			continue
		}
		one.tokens++
		if first.IsZero() {
			first = time.Now()
			one.ttft = first.Sub(start)
		}
	}
	if one.tokens == 0 {
		one.err = io.ErrUnexpectedEOF
		return
	}
	gen := time.Since(first)
	if gen > 0 && one.tokens > 1 {
		one.decode = float64(one.tokens-1) / gen.Seconds()
	} else {
		one.decode = float64(one.tokens)
	}
	return
}

// embeddingWords is the size of each embedding input: about 300 tokens, which
// fits an embedding model with a 512-token limit.
const embeddingWords = 300

// runEmbeddingLevel sends n concurrent embedding requests to the local engine
// and reports the slowest and the inputs embedded per second.
func runEmbeddingLevel(ctx context.Context, client *http.Client, baseURL, model string, n int) LevelResult {
	latency := make([]time.Duration, n)
	errs := make([]error, n)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			latency[i], errs[i] = embedOnce(ctx, client, baseURL, model)
		}(i)
	}
	wg.Wait()
	wall := time.Since(start)
	res := LevelResult{N: n}
	done := 0
	for i, err := range errs {
		if err != nil {
			res.Errors++
			continue
		}
		done++
		if latency[i] > res.P95Latency {
			res.P95Latency = latency[i]
		}
	}
	if wall > 0 {
		res.InputsPerSecond = float64(done) / wall.Seconds()
	}
	return res
}

// embedOnce embeds one input and returns how long the answer took. An answer
// without an embedding is an error.
func embedOnce(ctx context.Context, client *http.Client, baseURL, model string) (time.Duration, error) {
	body, _ := json.Marshal(map[string]any{"model": model, "input": embeddingInput()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			Embedding json.RawMessage `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return 0, err
	}
	if len(out.Data) == 0 || len(out.Data[0].Embedding) <= len("[]") {
		return 0, errors.New("no embedding in the answer")
	}
	return time.Since(start), nil
}

func embeddingInput() string {
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	return hex.EncodeToString(rnd[:]) + " " + strings.Repeat("hello ", embeddingWords)
}

func prompt() string {
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	return hex.EncodeToString(rnd[:]) + " " + strings.Repeat("hello ", promptWords)
}
