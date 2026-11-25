package compat

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Flockadoo/flockadoo/pkg/engine"
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
	// Share, Total and Usable are set when vLLM on unified memory may use
	// only a share of the memory (E2): Usable is Share × Total.
	Share  float64
	Total  int64
	Usable int64
}

func (e *DoesNotFit) Error() string {
	at := ""
	if e.Context > 0 {
		at = " at a " + groupDigits(e.Context) + "-token context"
	}
	var room string
	switch {
	case e.Share > 0:
		room = fmt.Sprintf("vLLM can use %s: a share of %.2f of the %s of memory shared by the CPU and GPU, leaving room for the system (%s is free)",
			formatBytes(e.Usable), e.Share, formatBytes(e.Total), formatBytes(e.Free))
	case e.Kind == MemoryUnified:
		room = formatBytes(e.Free) + " of memory (shared by the CPU and GPU) is free"
	default:
		room = formatBytes(e.Free) + " of GPU memory is free"
	}
	// No "lower the context": a context below the catalog's max_model_len
	// fails verification.
	return fmt.Sprintf("%s needs about %s to start%s; %s. Close other programs, or choose a smaller model",
		e.Model, formatBytes(e.Need), at, room)
}

// StartCheck compares what a model needs to start (its weights plus one
// request at its full context, the estimate the compatibility check uses)
// with the memory free for the engine now: free VRAM on a dedicated GPU, free
// system memory on unified memory, and for vLLM on unified memory the share of
// it vLLM starts with (E2). On a Mac vLLM is vLLM Metal, which gets no share:
// it sizes its KV cache to the memory free at start itself, so the model must
// fit the free memory. maxModelLen is the configured context; 0 uses the
// catalog's. It returns *DoesNotFit when the need is larger, and nil when the
// model fits or the client cannot tell: no free-memory reading, no GPU,
// several GPUs (the engine may split the model), or no model size in the
// catalog.
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
	usable, share := hw.MemoryFreeBytes, 0.0
	// vLLM (CUDA) on unified memory starts with a share of the memory that
	// leaves room for the system (E2); the model must fit that share. The
	// catalog says "vllm" for vLLM Metal too, so a Mac is told apart by its OS.
	if hw.MemoryKind == MemoryUnified && hw.OS != "darwin" && strings.EqualFold(strings.TrimSpace(entry.ServiceType), "vllm") {
		share = engine.MemoryShare(hw.MemoryTotalBytes, hw.MemoryFreeBytes)
		usable = hw.MemoryTotalBytes * int64(share*100+0.5) / 100
	}
	if need <= usable {
		return nil
	}
	if lenPtr == nil || !contextFit(entry.ServiceType) {
		ctxLen = 0
	}
	dnf := &DoesNotFit{Model: entry.Alias, Need: need, Free: hw.MemoryFreeBytes, Context: ctxLen, Kind: hw.MemoryKind}
	if share > 0 {
		dnf.Share, dnf.Total, dnf.Usable = share, hw.MemoryTotalBytes, usable
	}
	return dnf
}

// groupDigits is 32768 as "32,768".
func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
