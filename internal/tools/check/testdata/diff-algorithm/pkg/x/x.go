// Package x is the fixture for TestLintJudgesTheSameLinesWhateverTheDiffAlgorithm.
// The test commits sudo.go.base as sudo.go, then sudo.go.head over it, and
// lints only what changed between the two. The two files are the
// pkg/inferscale/up.go change that lint judged differently in CI (git's
// default myers diff) and locally (diff.algorithm=histogram).
package x

func euid() int { return 1000 }

func rootArgv(euid int, name string, args ...string) (string, []string) {
	if euid == 0 {
		return name, args
	}
	return "sudo", append([]string{"-n", "--", name}, args...)
}
