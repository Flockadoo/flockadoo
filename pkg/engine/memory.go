package engine

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// ErrNotEnoughMemory marks an engine start that failed for memory (engine
// start memory plan, E3).
var ErrNotEnoughMemory = errors.New("not enough memory")

// memoryError is a memory refusal in plain words.
type memoryError struct{ msg string }

func (e *memoryError) Error() string { return e.msg }
func (e *memoryError) Unwrap() error { return ErrNotEnoughMemory }

// The memory refusals the R runs recorded (2026-11-24 and 2026-11-25). vLLM's
// other memory errors are added when a run records them.
var (
	// vLLM's start-up check: the free memory is below its memory share.
	shareRefusal = regexp.MustCompile(`Free memory on device (\S+) \(([\d.]+)/([\d.]+) GiB\) on startup is less than desired GPU memory utilization \(([\d.]+), ([\d.]+) GiB\)`)
	// vLLM's KV cache check (upstream, so CUDA and Metal): the model loaded,
	// and the memory left for the KV cache does not hold one request at the
	// model's context.
	kvCacheTooSmall = regexp.MustCompile(`To serve at least one request with the model's max seq len \((\d+)\), \(([\d.]+) GiB KV cache is needed, which is larger than the available KV cache memory \(([\d.]+) GiB\)`)
	// vLLM Metal: the GPU ran out of memory while loading.
	metalOutOfMemory = "[METAL] Command buffer execution failed: Insufficient Memory"
)

// logTail is how much of what a start wrote is read: the refusal is near the
// end, and a start that downloaded can write a lot before it.
const logTail = 4 << 20

// memoryRefusal reads engine log text for vLLM's memory refusals and says, in
// plain words, what it means.
func memoryRefusal(log string) (string, bool) {
	if m := shareRefusal.FindStringSubmatch(log); m != nil {
		return fmt.Sprintf("vLLM did not start: it asks for %s GiB (%s of the %s GiB on %s) and only %s GiB is free. Close programs that use GPU memory, or choose a smaller model",
			m[5], m[4], m[3], m[1], m[2]), true
	}
	// vLLM goes on to suggest a shorter max_model_len; a context below the
	// catalog's fails verification, so that is not passed on.
	if m := kvCacheTooSmall.FindStringSubmatch(log); m != nil {
		return fmt.Sprintf("vLLM did not start: one request at the model's %s-token context needs %s GiB of KV cache, and only %s GiB is left for it after loading the model. Close other programs (and any orphaned vLLM engine process), or choose a smaller model",
			groupThousands(m[1]), m[2], m[3]), true
	}
	if strings.Contains(log, metalOutOfMemory) {
		return "vLLM ran out of memory on this Mac's GPU while loading the model. Close other programs (and any orphaned vLLM engine process), or choose a smaller model", true
	}
	return "", false
}

// LogOffset is the engine log's size now: what a start writes comes after it.
func LogOffset(logPath string) int64 {
	st, err := os.Stat(logPath)
	if err != nil {
		return 0
	}
	return st.Size()
}

// ExplainStartFailure returns err, or, when what the engine wrote to its log
// since offset shows a memory refusal, the refusal in plain words (wrapping
// ErrNotEnoughMemory).
func ExplainStartFailure(err error, logPath string, offset int64) error {
	if err == nil {
		return nil
	}
	f, openErr := os.Open(logPath)
	if openErr != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if size := LogOffset(logPath); size-offset > logTail {
		offset = size - logTail
	}
	if _, seekErr := f.Seek(offset, io.SeekStart); seekErr != nil {
		return err
	}
	written, readErr := io.ReadAll(io.LimitReader(f, logTail))
	if readErr != nil {
		return err
	}
	if msg, ok := memoryRefusal(string(written)); ok {
		return &memoryError{msg: msg}
	}
	return err
}

// groupThousands writes a whole number with commas: 131072 is 131,072.
func groupThousands(digits string) string {
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}
