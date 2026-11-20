package winglink

import "testing"

// Plan item Q3: the tailnet's status JSON is output the client did not write.
func FuzzParseStatus(f *testing.F) {
	f.Add([]byte(`{"BackendState":"Running","Self":{"ID":"n1","Online":true,"TailscaleIPs":["100.64.0.1","fd7a::1"]}}`))
	f.Add([]byte(`{"Self":null}`))
	f.Add([]byte(`not json`))
	f.Fuzz(func(t *testing.T, out []byte) {
		_, _ = parseStatus(out)
	})
}
