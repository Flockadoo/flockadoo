package server

import (
	"testing"
	"time"
)

// Plan item Q3: the stream deadline header comes from the platform.
func FuzzStreamWriteDeadline(f *testing.F) {
	f.Add("600000")
	f.Add("-1")
	f.Add("9223372036854775807")
	f.Add(" 12 ")
	f.Fuzz(func(t *testing.T, header string) {
		arrived := time.Unix(1_700_000_000, 0)
		if when, ok := streamWriteDeadline(header, arrived); ok && !when.After(arrived) {
			t.Fatalf("header %q gave a deadline %v not after the arrival %v", header, when, arrived)
		}
	})
}

// Found by FuzzStreamWriteDeadline (plan item Q3): a header of 10^13 ms or
// more overflowed the duration and put the deadline centuries in the past,
// so every write of the stream failed at once. An absurd value is capped.
func TestAnAbsurdStreamDeadlineIsCapped(t *testing.T) {
	arrived := time.Unix(1_700_000_000, 0)
	for _, header := range []string{"010000000000000", "9223372036854775807", "86400001"} {
		when, ok := streamWriteDeadline(header, arrived)
		if !ok {
			t.Fatalf("header %q gave no deadline", header)
		}
		if !when.After(arrived) || when.After(arrived.Add(maxStreamDuration+streamWriteDeadlineGrace)) {
			t.Fatalf("header %q gave %v, want after arrival and within %v of it", header, when, maxStreamDuration)
		}
	}
}
