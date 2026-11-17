package setup

import (
	"strings"
	"testing"
)

// Plan item Q3: a join code is pasted by a person, with whatever came with it.
func FuzzCredential(f *testing.F) {
	f.Add("join-abc123")
	f.Add("Join code: `join-abc123`.")
	f.Add(`"sk-key"`)
	f.Fuzz(func(t *testing.T, s string) {
		got := credential(s)
		if strings.HasPrefix(got, "join-") && strings.ContainsAny(got, " \t\n") {
			t.Fatalf("credential(%q) = %q, a join code with whitespace in it", s, got)
		}
	})
}
