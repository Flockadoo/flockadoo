package nodeapi

import "testing"

// Plan item Q3: release versions come from the server.
func FuzzNewer(f *testing.F) {
	f.Add("1.2.3", "1.2.2")
	f.Add("v0.43.0", "dev")
	f.Add("1.2", "1.2.0-rc1+dirty")
	f.Fuzz(func(t *testing.T, a, b string) {
		if Newer(a, b) && Newer(b, a) {
			t.Fatalf("%q and %q are each newer than the other", a, b)
		}
		if Newer(a, a) {
			t.Fatalf("%q is newer than itself", a)
		}
	})
}

// Plan item Q3: a 426 body comes from the server.
func FuzzParseUpgrade(f *testing.F) {
	f.Add([]byte(`{"error":"client upgrade required","min_version":"1.2.0","latest_version":"1.3.0"}`))
	f.Add([]byte(`{"min_version":12}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if ParseUpgrade(426, body) == nil {
			t.Fatal("a 426 gave no upgrade error")
		}
	})
}
