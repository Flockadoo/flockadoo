package engine

// DefaultMemoryShare is vLLM's own --gpu-memory-utilization: the share of the
// GPU's memory it reserves when the command sets none.
const DefaultMemoryShare = 0.92

// MemoryShare is the --gpu-memory-utilization for vLLM on unified memory,
// from the machine's total memory and the memory free now (engine start
// memory plan, E2).
func MemoryShare(total, free int64) float64 { return 0 }

// ShareMargin is the memory left to the system on unified memory.
func ShareMargin(total int64) int64 { return 0 }

// MemoryShareOf is the --gpu-memory-utilization a command sets.
func MemoryShareOf(args []string) (float64, bool) { return 0, false }

// WithMemoryShare is s started with --gpu-memory-utilization share.
func (s Spec) WithMemoryShare(share float64) Spec { return s }

// StartOption adjusts the command StartAndWait is about to start.
type StartOption func(kind Kind, spec *Spec)
