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

// Plan item Q3: nvidia-smi's XML is output the client did not write.
func FuzzParseNvidiaSMI(f *testing.F) {
	f.Add([]byte(`<nvidia_smi_log><attached_gpus>1</attached_gpus><gpu id="0"><product_name>X</product_name><fb_memory_usage><total>80 GiB</total></fb_memory_usage></gpu></nvidia_smi_log>`))
	f.Add([]byte(`<nvidia_smi_log><attached_gpus>-3</attached_gpus></nvidia_smi_log>`))
	f.Add([]byte(`<<<`))
	f.Fuzz(func(t *testing.T, out []byte) {
		if info, err := parseNvidiaSMI(out); err == nil && info.GPUCount < 1 {
			t.Fatalf("accepted %d GPUs", info.GPUCount)
		}
	})
}
