package analysis

import (
	"fmt"
	"strings"
	"time"

	"github.com/dkolancs/crashlab/internal/model"
)

// RegressionNote describes how this run differs from the last passing run of the same name.
// The wording records a difference. It does not assign a cause.
func RegressionNote(previousID int64, previous, current Outcome) string {
	if previous.Baseline.Count < MinSamples || previous.Failure.Count < MinSamples {
		return ""
	}
	if current.Baseline.Count < MinSamples || current.Failure.Count < MinSamples {
		return ""
	}
	var reasons []string
	if Failed(current.Assertions) {
		reasons = append(reasons, "an assertion failed")
	}
	if current.Failure.ErrorRate >= previous.Failure.ErrorRate+0.05 {
		reasons = append(reasons, fmt.Sprintf("failure error rate is %.1f%%, up from %.1f%%", current.Failure.ErrorRate*100, previous.Failure.ErrorRate*100))
	}
	if previous.RecoveryMs != nil && current.RecoveryMs != nil && *current.RecoveryMs >= *previous.RecoveryMs+2000 {
		reasons = append(reasons, fmt.Sprintf("recovery time is %.1fs, up from %.1fs", float64(*current.RecoveryMs)/1000, float64(*previous.RecoveryMs)/1000))
	}
	if len(reasons) == 0 {
		return ""
	}
	return fmt.Sprintf("Regression against passing run #%03d: %s. This records a difference from that run, not a cause.", previousID, strings.Join(reasons, "; "))
}

func filterTraceWindow(samples []model.Sample, injectedAt, removedAt time.Time) []model.Sample {
	if injectedAt.IsZero() {
		return dropTraces(samples)
	}
	out := make([]model.Sample, 0, len(samples))
	for _, sm := range samples {
		if sm.Source != model.SourceTrace {
			out = append(out, sm)
			continue
		}
		if sm.At.Before(injectedAt) {
			continue
		}
		if !removedAt.IsZero() && sm.At.After(removedAt) {
			continue
		}
		out = append(out, sm)
	}
	return out
}

func dropTraces(samples []model.Sample) []model.Sample {
	out := make([]model.Sample, 0, len(samples))
	for _, sm := range samples {
		if sm.Source != model.SourceTrace {
			out = append(out, sm)
		}
	}
	return out
}
