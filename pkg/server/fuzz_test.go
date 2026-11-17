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
