package capprobe

import "testing"

// Plan item Q3: a probe's answer is engine output the client did not write.
func FuzzProbePassed(f *testing.F) {
	for _, name := range Names {
		f.Add(name, []byte(`{"choices":[{"message":{"content":"{\"a\":1}","tool_calls":[{"function":{"name":"get_weather"}}]}}]}`))
	}
	f.Add("tools", []byte(`{"choices":null}`))
	f.Fuzz(func(t *testing.T, name string, raw []byte) {
		_ = probePassed(name, raw)
	})
}
