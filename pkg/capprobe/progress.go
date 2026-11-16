package capprobe

import "fmt"

const probeCount = 6

// reasoningCheck is the index of the reasoning check in Names. It runs only
// for a model the catalog gives a reasoning parser.
const reasoningCheck = 5

// Names are the checks, in order.
var Names = []string{"tools", "forced_tools", "json_object", "json_schema", "logprobs", "reasoning"}

// Outcome is one probe result. Empty means not checked yet.
type Outcome string

const (
	Waiting     Outcome = ""
	Supported   Outcome = "supported"
	Unsupported Outcome = "unsupported"
	Unverified  Outcome = "unverified"
	// NotApplicable is a check that does not apply to this model (reasoning,
	// for a model that does not think). It is not run or reported.
	NotApplicable Outcome = "n/a"
)

// Label is the status line. Checked counts supported, unsupported, and
// unverified, out of the checks that apply.
func Label(outcomes []Outcome) string {
	if len(outcomes) > probeCount {
		outcomes = outcomes[:probeCount]
	}
	total, checked, unverified := 0, 0, 0
	for _, o := range outcomes {
		if o == NotApplicable {
			continue
		}
		total++
		if o == Waiting {
			continue
		}
		checked++
		if o == Unverified {
			unverified++
		}
	}
	if checked == 0 {
		return ""
	}
	line := fmt.Sprintf("testing - %d/%d checked (still serving requests)", checked, total)
	if checked == total {
		line = "completed"
		if unverified > 0 {
			line = fmt.Sprintf("completed (%d unverified, retrying)", unverified)
		}
	}
	return line
}
