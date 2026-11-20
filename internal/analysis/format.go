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
	fmt.Fprintf(&b, "+%s network latency\n", exp.Delay)
	fmt.Fprintf(&b, "%s\n\n", humanDuration(exp.Duration))

	writeWindow(&b, "BASELINE", out.Baseline, out.CPUBaseline)
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
	fmt.Fprintf(&b, "\nTimestamp correlation along declared dependencies.\n\n")

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
	if out.RecoveryMs != nil {
		sec := float64(*out.RecoveryMs) / 1000
		fmt.Fprintf(&b, "Recovery time: %.1f seconds\n", sec)
	} else if out.Degraded {
		fmt.Fprintf(&b, "Recovery time: not observed\n")
	}
	if out.RetryFactor >= 2 {
		fmt.Fprintf(&b, "Target receive rate: %.1fx baseline\n", out.RetryFactor)
	}
	return b.String()
}

func writeWindow(b *strings.Builder, title string, w Window, cpu float64) {
	fmt.Fprintf(b, "%s\n", title)
	fmt.Fprintf(b, "────────────────────────────\n")
	fmt.Fprintf(b, "%-16s %s\n", "p50 latency", formatLatency(w.P50))
	fmt.Fprintf(b, "%-16s %s\n", "p95 latency", formatLatency(w.P95))
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
