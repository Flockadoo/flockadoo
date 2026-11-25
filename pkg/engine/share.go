package engine

import (
	"slices"
	"strconv"
	"strings"
)

// DefaultMemoryShare is vLLM's own --gpu-memory-utilization: the share of the
// GPU's memory it reserves when the command sets none.
const DefaultMemoryShare = 0.92

// minShareMargin is the least memory left to the system on unified memory.
const minShareMargin = 4 << 30

const shareFlag = "--gpu-memory-utilization"

// ShareMargin is the memory left to the system on unified memory: 4 GiB or 5%
// of the total, whichever is larger.
func ShareMargin(total int64) int64 {
	return max(total/20, minShareMargin)
}

// MemoryShare is the --gpu-memory-utilization for vLLM on unified memory
// (engine start memory plan, E2): the share of total that fits the memory free
// now, less ShareMargin, rounded down to 0.01 and at most vLLM's default. It
// is 0 when no share fits, or without a reading.
func MemoryShare(total, free int64) float64 {
	if total <= 0 || free <= 0 {
		return 0
	}
	usable := free - ShareMargin(total)
	if usable <= 0 {
		return 0
	}
	hundredths := usable * 100 / total
	switch {
	case hundredths >= 92:
		return DefaultMemoryShare
	case hundredths < 1:
		return 0
	}
	return float64(hundredths) / 100
}

// MemoryShareOf is the --gpu-memory-utilization a command sets.
func MemoryShareOf(args []string) (float64, bool) {
	for i, a := range args {
		val, ok := "", false
		switch {
		case a == shareFlag && i+1 < len(args):
			val, ok = args[i+1], true
		case strings.HasPrefix(a, shareFlag+"="):
			val, ok = strings.TrimPrefix(a, shareFlag+"="), true
		}
		if ok {
			share, err := strconv.ParseFloat(val, 64)
			return share, err == nil
		}
	}
	return 0, false
}

// WithMemoryShare is s started with --gpu-memory-utilization share. A command
// that already sets a share keeps it, and a share of 0 or at least vLLM's
// default leaves the command as it is. s is not modified.
func (s Spec) WithMemoryShare(share float64) Spec {
	if share <= 0 || share >= DefaultMemoryShare {
		return s
	}
	if _, ok := MemoryShareOf(s.Args); ok {
		return s
	}
	s.Args = append(slices.Clone(s.Args), shareFlag, strconv.FormatFloat(share, 'f', 2, 64))
	return s
}

// StartOption adjusts the command StartAndWait is about to start.
type StartOption func(kind Kind, spec *Spec)
