// Package hardware reads facts about the machine that more than one package
// needs: the system's memory, and how much of an NVIDIA unified-memory
// machine's memory is usable.
package hardware

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// NvidiaUnifiedUsableFraction is the share of an NVIDIA unified-memory
// machine's memory (GB10) scored as usable. It is the share every R1 model
// started with on peipei, with its desktop running (engine start memory
// plan, E1; confirmed on the machine in R). The compatibility score and the
// health report both use it, so the memory a GB10 reports is the memory it
// is scored with.
const NvidiaUnifiedUsableFraction = 0.80

// MemInfo is the system's memory in bytes: MemTotal and MemAvailable on Linux.
type MemInfo struct {
	Total     int64
	Available int64
}

// ErrMemInfoUnsupported is ReadMemInfo off Linux, the one platform with
// NVIDIA unified memory (GB10).
var ErrMemInfoUnsupported = errors.New("available memory is read only on Linux")

// ParseMemInfo reads MemTotal and MemAvailable from /proc/meminfo's format.
// Both must be there: a missing MemAvailable is an error, never 0 bytes free.
func ParseMemInfo(r io.Reader) (MemInfo, error) {
	var m MemInfo
	var haveTotal, haveAvailable bool
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		var dst *int64
		switch fields[0] {
		case "MemTotal:":
			dst, haveTotal = &m.Total, true
		case "MemAvailable:":
			dst, haveAvailable = &m.Available, true
		default:
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return MemInfo{}, fmt.Errorf("meminfo %s: %w", fields[0], err)
		}
		*dst = kb * 1024
	}
	if err := sc.Err(); err != nil {
		return MemInfo{}, err
	}
	if !haveTotal || !haveAvailable || m.Total <= 0 {
		return MemInfo{}, errors.New("meminfo has no MemTotal or MemAvailable")
	}
	return m, nil
}
