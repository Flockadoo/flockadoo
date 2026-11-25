package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Engine start memory plan, item E3. The client's estimate is approximate, so
// vLLM can still refuse to start for memory. The client recognises vLLM's
// memory refusals in what the engine wrote to its log during this start and
// says what they mean in plain words, with vLLM's numbers.
//
// The fixtures are lines the R1 runs wrote (2026-11-24), copied, not written:
// peipei (GB10, vLLM 0.34.0, bge-m3 at vLLM's default memory share) and the
// macmini (M4, vLLM Metal 0.33.0, Qwen2.5-3B with an orphan holding 6 GB).
// vLLM's other memory errors (CUDA out of memory, a KV cache too small for the
// context) are added when a run records them.
const (
	peipeiRefusal  = "(EngineCore pid=2132371) ERROR 11-24 09:15:32 [core.py:1757] ValueError: Free memory on device cuda:0 (105.25/121.86 GiB) on startup is less than desired GPU memory utilization (0.92, 112.11 GiB). Decrease GPU memory utilization or reduce GPU memory used by other processes."
	macminiRefusal = "(EngineCore pid=309) ERROR 11-24 10:11:13 [core.py:1584] RuntimeError: [METAL] Command buffer execution failed: Insufficient Memory (00000008:kIOGPUCommandBufferCallbackErrorOutOfMemory)."
)

func TestVLLMsMemoryShareRefusalIsExplained(t *testing.T) {
	msg, ok := memoryRefusal("starting\n" + peipeiRefusal + "\nmore output\n")
	if !ok {
		t.Fatal("the refusal was not recognised")
	}
	for _, want := range []string{"112.11 GiB", "0.92", "121.86 GiB", "105.25 GiB", "Close programs"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q does not say %q", msg, want)
		}
	}
}

func TestMetalOutOfMemoryIsExplained(t *testing.T) {
	msg, ok := memoryRefusal(macminiRefusal + "\n")
	if !ok {
		t.Fatal("the refusal was not recognised")
	}
	for _, want := range []string{"ran out of memory", "Mac", "Close other programs", "smaller model"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q does not say %q", msg, want)
		}
	}
}

// Negative: a log without a memory refusal is not called a memory problem.
func TestOtherFailuresAreNotMemory(t *testing.T) {
	for _, log := range []string{"", "the engine exited for another reason\n", "memory\nGiB\n"} {
		if msg, ok := memoryRefusal(log); ok {
			t.Errorf("memoryRefusal(%q) = %q", log, msg)
		}
	}
}

func TestOnlyWhatThisStartWroteCounts(t *testing.T) {
	log := filepath.Join(t.TempDir(), "engine.log")
	// An earlier start's refusal is already in the log.
	if err := os.WriteFile(log, []byte(peipeiRefusal+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	offset := LogOffset(log)
	if offset != int64(len(peipeiRefusal)+1) {
		t.Fatalf("LogOffset = %d, want the log's size", offset)
	}
	appendLog := func(line string) {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
	appendLog("the engine exited for another reason")

	failed := errors.New("engine exited")
	if got := ExplainStartFailure(failed, log, offset); got != failed {
		t.Errorf("ExplainStartFailure = %v, want the original error: the refusal is from an earlier start", got)
	}

	appendLog(macminiRefusal)
	got := ExplainStartFailure(failed, log, offset)
	if !errors.Is(got, ErrNotEnoughMemory) || !strings.Contains(got.Error(), "ran out of memory") {
		t.Errorf("ExplainStartFailure = %v, want the memory refusal", got)
	}
}

// A log that does not exist yet has size 0, and explains nothing.
func TestAMissingLogExplainsNothing(t *testing.T) {
	log := filepath.Join(t.TempDir(), "none.log")
	if got := LogOffset(log); got != 0 {
		t.Errorf("LogOffset = %d, want 0", got)
	}
	failed := errors.New("engine exited")
	if got := ExplainStartFailure(failed, log, 0); got != failed {
		t.Errorf("ExplainStartFailure = %v, want the original error", got)
	}
}
