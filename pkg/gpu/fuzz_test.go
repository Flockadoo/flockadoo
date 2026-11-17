package gpu

import "testing"

// Plan item Q3: ioreg's output is text the client did not write.
func FuzzParseIORegAccelerator(f *testing.F) {
	f.Add(`"PerformanceStatistics" = {"Device Utilization %"=37,"In use system memory"=123456}`)
	f.Add(`"Device Utilization %"=99999999999999999999`)
	f.Fuzz(func(t *testing.T, out string) {
		_, _ = parseIORegAccelerator(out)
	})
}
