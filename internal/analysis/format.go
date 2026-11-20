package analysis

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/dkolancs/crashlab/internal/model"
)

// Format renders the text report shown in the CLI and the dashboard.
func Format(exp model.Experiment, out Outcome, events []model.Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CRASHLAB EXPERIMENT #%03d\n\n", exp.ID)
	fmt.Fprintf(&b, "FAULT\n")
	fmt.Fprintf(&b, "────────────────────────────\n")
	fmt.Fprintf(&b, "%s\n", exp.Target)
	fmt.Fprintf(&b, "%s\n", faultDescription(exp))
	fmt.Fprintf(&b, "%s\n\n", humanDuration(exp.Duration))

	writeWindow(&b, "BASELINE", out.Baseline, out.CPUBaseline)
	if out.OfferedRate > 0 || out.CompletedRate > 0 {
		fmt.Fprintf(&b, "%-16s %.1f rps\n", "offered rate", out.OfferedRate)
		fmt.Fprintf(&b, "%-16s %.1f rps\n\n", "completed rate", out.CompletedRate)
	}
	writeWindow(&b, "DURING FAILURE", out.Failure, out.CPUFailure)
	writeWindow(&b, "RECOVERY", out.Recovery, 0)

	fmt.Fprintf(&b, "OBSERVED PROPAGATION\n")
	fmt.Fprintf(&b, "────────────────────────────\n")
	if len(out.Hops) == 0 {
		fmt.Fprintf(&b, "none\n")
	} else {
		for i, hop := range out.Hops {
			if i > 0 {
				fmt.Fprintf(&b, "      ↓\n")
			}
			fmt.Fprintf(&b, "%s\n", hop.Service)
		}
	}
	note := out.PathNote
	if note == "" {
		note = "Timestamp correlation along declared dependencies."
	}
	fmt.Fprintf(&b, "\n%s\n\n", note)

	fmt.Fprintf(&b, "EVENTS\n")
	fmt.Fprintf(&b, "────────────────────────────\n")
	sorted := append([]model.Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].At.Equal(sorted[j].At) {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].At.Before(sorted[j].At)
	})
	if len(sorted) == 0 {
		fmt.Fprintf(&b, "none\n")
	}
	for _, ev := range sorted {
		fmt.Fprintf(&b, "%s  %s\n", clockOffset(exp.StartedAt, ev.At), ev.Message)
	}
	fmt.Fprintf(&b, "\nRESULT\n")
	fmt.Fprintf(&b, "────────────────────────────\n")
	fmt.Fprintf(&b, "%s\n", out.Conclusion)
	switch {
	case out.Conclusion == "Not enough data":
		fmt.Fprintf(&b, "Recovery time: not enough data\n")
	case out.RecoveryMs != nil:
		fmt.Fprintf(&b, "Recovery time: %.1f seconds\n", float64(*out.RecoveryMs)/1000)
	case out.Degraded:
		fmt.Fprintf(&b, "Recovery time: not observed\n")
	}
	if out.RetryFactor >= 2 {
		fmt.Fprintf(&b, "Target receive rate: %.1fx baseline\n", out.RetryFactor)
	}
	if out.Explanation != "" {
		fmt.Fprintf(&b, "\nEXPLANATION\n")
		fmt.Fprintf(&b, "────────────────────────────\n")
		fmt.Fprintf(&b, "%s\n", out.Explanation)
	}
	if len(out.Assertions) > 0 {
		fmt.Fprintf(&b, "\nASSERTIONS\n")
		fmt.Fprintf(&b, "────────────────────────────\n")
		failed := false
		for _, check := range out.Assertions {
			label := "PASS"
			if !check.Pass {
				label = "FAIL"
				failed = true
			}
			fmt.Fprintf(&b, "%s %s\n", label, check.Detail)
		}
		if failed {
			fmt.Fprintf(&b, "\nExperiment FAILED\n")
		} else {
			fmt.Fprintf(&b, "\nExperiment PASSED\n")
		}
	}
	return b.String()
}

func faultDescription(exp model.Experiment) string {
	switch exp.FaultType {
	case model.FaultLoss:
		return fmt.Sprintf("%.0f%% packet loss", exp.LossPercent)
	case model.FaultKill:
		return "container terminated"
	case model.FaultCPU:
		return fmt.Sprintf("%.0f%% cpu", exp.CPUPercent)
	default:
		if exp.Delay == 0 {
			return "+800ms network latency"
		}
		return fmt.Sprintf("+%s network latency", exp.Delay)
	}
}

func writeWindow(b *strings.Builder, title string, w Window, cpu float64) {
	p50, p95 := "n/a", "n/a"
	if w.SuccessCount > 0 {
		p50, p95 = formatLatency(w.P50), formatLatency(w.P95)
	}
	fmt.Fprintf(b, "%s\n", title)
	fmt.Fprintf(b, "────────────────────────────\n")
	fmt.Fprintf(b, "%-16s %s\n", "p50 latency", p50)
	fmt.Fprintf(b, "%-16s %s\n", "p95 latency", p95)
	if w.Count > w.SuccessCount && w.Count > 0 {
		fmt.Fprintf(b, "%-16s %s\n", "p95 all", formatLatency(w.P95All))
	}
	fmt.Fprintf(b, "%-16s %s\n", "error rate", formatPercent(w.ErrorRate))
	if cpu > 0 {
		fmt.Fprintf(b, "%-16s %.0f%%\n", "cpu", cpu)
	}
	fmt.Fprintf(b, "\n")
}

func formatLatency(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1f sec", ms/1000)
	}
	return fmt.Sprintf("%.0f ms", math.Round(ms))
}

func formatPercent(rate float64) string {
	return fmt.Sprintf("%.1f%%", rate*100)
}

func humanDuration(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	return d.String()
}

func clockOffset(start, at time.Time) string {
	d := at.Sub(start)
	if d < 0 {
		d = 0
	}
	total := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}
