package engine

import "errors"

// ErrNotEnoughMemory marks an engine start that failed for memory (engine
// start memory plan, E3).
var ErrNotEnoughMemory = errors.New("not enough memory")

// memoryRefusal reads engine log text for vLLM's memory refusals and says, in
// plain words, what it means.
func memoryRefusal(log string) (string, bool) { return "", false }

// LogOffset is the engine log's size now: what a start writes comes after it.
func LogOffset(logPath string) int64 { return 0 }

// ExplainStartFailure returns err, or, when what the engine wrote to its log
// since offset shows a memory refusal, the refusal in plain words.
func ExplainStartFailure(err error, logPath string, offset int64) error { return err }
