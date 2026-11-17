package relay

import "testing"

// Plan item B2, finding STR-2: model and stream are read once, from the top
// level of the body. A substring search found "stream":true inside a nested
// value and missed spaced JSON.
func TestTheEnvelopeIsReadFromTheTopLevel(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		want   Envelope
		hasErr bool
	}{
		{"stream", `{"model":"m","stream":true}`, Envelope{Model: "m", Stream: true}, false},
		{"spaced", `{ "model" : "m" , "stream" : true }`, Envelope{Model: "m", Stream: true}, false},
		{"not streamed", `{"model":"m","stream":false}`, Envelope{Model: "m"}, false},
		{"absent", `{"model":"m"}`, Envelope{Model: "m"}, false},
		{"nested match", `{"model":"m","response_format":{"json_schema":{"schema":{"default":{"stream":true}}}}}`, Envelope{Model: "m"}, false},
		{"in a message", `{"model":"m","messages":[{"role":"user","content":"\"stream\":true"}]}`, Envelope{Model: "m"}, false},
		{"not JSON", `model=m&stream=true`, Envelope{}, true},
		{"an array", `[{"stream":true}]`, Envelope{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseEnvelope([]byte(tc.body))
			if (err != nil) != tc.hasErr {
				t.Fatalf("error = %v, want error %v", err, tc.hasErr)
			}
			if got != tc.want {
				t.Fatalf("envelope = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Plan item Q3 named this target for B2: the body is bytes the client did
// not write. Parsing never panics, and a parsed envelope agrees with itself.
func FuzzParseEnvelope(f *testing.F) {
	f.Add([]byte(`{"model":"m","stream":true}`))
	f.Add([]byte(`{"stream":"yes","model":7}`))
	f.Add([]byte(`{"model":"m","stream":true,"stream":false}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		a, errA := ParseEnvelope(body)
		b, errB := ParseEnvelope(body)
		if a != b || (errA == nil) != (errB == nil) {
			t.Fatalf("two parses disagree: %+v %v, %+v %v", a, errA, b, errB)
		}
	})
}
