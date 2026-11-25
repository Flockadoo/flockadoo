package compat

import (
	"fmt"
	"strconv"

	"github.com/Flockadoo/flockadoo/pkg/verify"
)

// DoesNotFit is a model that needs more memory to start than is free for the
// engine (engine start memory plan, E3).
type DoesNotFit struct {
	Model string
	Need  int64
	Free  int64
	// Context is the context the need was computed at; 0 when none applies.
	Context int64
	Kind    MemoryKind
}

func (e *DoesNotFit) Error() string {
	at := ""
	if e.Context > 0 {
		at = " at a " + groupDigits(e.Context) + "-token context"
	}
	free := "of GPU memory is free"
	if e.Kind == MemoryUnified {
		free = "of memory (shared by the CPU and GPU) is free"
	}
	return fmt.Sprintf("%s needs about %s to start%s; %s %s. Close other programs, choose a smaller model, or lower the context",
		e.Model, formatBytes(e.Need), at, formatBytes(e.Free), free)
}

// StartCheck compares what a model needs to start (its weights plus one
// request at its full context, the estimate the compatibility check uses)
// with the memory free for the engine now: free VRAM on a dedicated GPU, free
// system memory on unified memory. maxModelLen is the configured context; 0
// uses the catalog's. It returns *DoesNotFit when the need is larger, and nil
// when the model fits or the client cannot tell: no free-memory reading (a
// Mac, for now), no GPU, several GPUs (the engine may split the model), or no
// model size in the catalog.
func StartCheck(hw *Hardware, entry verify.CatalogEntry, maxModelLen int64) error {
	if hw == nil || entry.MinSizeBytes <= 0 || hw.MemoryFreeBytes <= 0 || hw.GPUCount != 1 {
		return nil
	}
	if hw.MemoryKind != MemoryVRAM && hw.MemoryKind != MemoryUnified {
		return nil
	}
	ctxLen := maxModelLen
	if ctxLen <= 0 && entry.MaxModelLen != nil {
		ctxLen = *entry.MaxModelLen
	}
	var lenPtr *int64
	if ctxLen > 0 {
		lenPtr = &ctxLen
	}
	need := requiredMemoryBytes(entry.MinSizeBytes, entry.ServiceType, lenPtr, entry.KVCacheBytesPerToken)
	if need <= hw.MemoryFreeBytes {
		return nil
	}
	if lenPtr == nil || !contextFit(entry.ServiceType) {
		ctxLen = 0
	}
	return &DoesNotFit{Model: entry.Alias, Need: need, Free: hw.MemoryFreeBytes, Context: ctxLen, Kind: hw.MemoryKind}
}

// groupDigits is 32768 as "32,768".
func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
