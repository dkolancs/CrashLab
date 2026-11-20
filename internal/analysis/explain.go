package analysis

import (
	"fmt"
	"strings"

	"github.com/dkolancs/crashlab/internal/model"
)

// Explain cites the measurements that describe how the fault spread.
func Explain(exp model.Experiment, out Outcome) string {
	if out.Baseline.Count == 0 || out.Failure.Count == 0 {
		return ""
	}
	var parts []string
	fault := faultDescription(exp)
	switch {
	case len(out.Hops) > 1:
		names := make([]string, len(out.Hops))
		for i, hop := range out.Hops {
			names[i] = hop.Service
		}
		parts = append(parts, fmt.Sprintf("%s on %s was followed by degradation at %s.", fault, exp.Target, strings.Join(names[1:], ", then ")))
	case out.Degraded:
		parts = append(parts, fmt.Sprintf("%s on %s changed user-facing traffic, with no further dependent departure.", fault, exp.Target))
	default:
		parts = append(parts, fmt.Sprintf("%s on %s stayed within the baseline spread.", fault, exp.Target))
	}
	parts = append(parts, fmt.Sprintf("Entry error rate moved from %s to %s. Entry p95 of successful calls moved from %s to %s.",
		formatPercent(out.Baseline.ErrorRate), formatPercent(out.Failure.ErrorRate),
		successP95(out.Baseline), successP95(out.Failure)))
	if out.RetryFactor >= 2 {
		parts = append(parts, fmt.Sprintf("%s receive rate rose to %.1fx its baseline during the fault.", exp.Target, out.RetryFactor))
	}
	if out.RecoveryMs != nil {
		parts = append(parts, fmt.Sprintf("Recovery took %.1fs after the fault was removed.", float64(*out.RecoveryMs)/1000))
	}
	return strings.Join(parts, " ")
}

func successP95(w Window) string {
	if w.SuccessCount == 0 {
		return "n/a"
	}
	return formatLatency(w.P95)
}
