package analysis

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SavedRun is one completed experiment and the outcome stored with it.
type SavedRun struct {
	ID            int64
	Name          string
	ExpectFailure bool
	Outcome       Outcome
}

// HistoryRun is one judged or known-bad run inside a name's history.
type HistoryRun struct {
	ID         int64    `json:"id"`
	RecoveryMs *int64   `json:"recoveryMs"`
	Details    []string `json:"details,omitempty"`
}

// NameHistory is the assertion record and recovery series for one experiment name.
type NameHistory struct {
	Name     string       `json:"name"`
	Judged   int          `json:"judged"`
	Passed   int          `json:"passed"`
	PassText string       `json:"passText"`
	Trend    string       `json:"trend"`
	Recovery []HistoryRun `json:"recovery"`
	Failed   []HistoryRun `json:"failed"`
	KnownBad []HistoryRun `json:"knownBad"`
}

// SavedFromSummary reads one stored report JSON into a history input.
func SavedFromSummary(id int64, name string, expectFailure bool, summary string) (SavedRun, error) {
	var out Outcome
	if err := json.Unmarshal([]byte(summary), &out); err != nil {
		return SavedRun{}, err
	}
	return SavedRun{ID: id, Name: name, ExpectFailure: expectFailure, Outcome: out}, nil
}

// History groups completed runs by name.
// Known-bad runs (expect.mode fail) are listed and left out of the pass rate.
func History(runs []SavedRun) []NameHistory {
	byName := map[string][]SavedRun{}
	for _, run := range runs {
		if run.Name == "" {
			continue
		}
		byName[run.Name] = append(byName[run.Name], run)
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]NameHistory, 0, len(names))
	for _, name := range names {
		group := byName[name]
		sort.Slice(group, func(i, j int) bool { return group[i].ID < group[j].ID })
		out = append(out, historyFor(name, group))
	}
	return out
}

func historyFor(name string, runs []SavedRun) NameHistory {
	item := NameHistory{
		Name: name, PassText: "not established", Trend: "trend is not established",
		Recovery: []HistoryRun{}, Failed: []HistoryRun{}, KnownBad: []HistoryRun{},
	}
	for _, run := range runs {
		ref := HistoryRun{ID: run.ID, RecoveryMs: run.Outcome.RecoveryMs, Details: failedDetails(run.Outcome)}
		if run.ExpectFailure {
			item.KnownBad = append(item.KnownBad, ref)
			continue
		}
		if !judgedOutcome(run.Outcome) {
			continue
		}
		item.Judged++
		item.Recovery = append(item.Recovery, HistoryRun{ID: run.ID, RecoveryMs: run.Outcome.RecoveryMs})
		if Failed(run.Outcome.Assertions) {
			item.Failed = append(item.Failed, ref)
			continue
		}
		item.Passed++
	}
	if item.Judged > 0 {
		item.PassText = fmt.Sprintf("%d/%d", item.Passed, item.Judged)
	}
	item.Trend = recoveryTrend(item.Recovery)
	return item
}

func judgedOutcome(out Outcome) bool {
	return out.Baseline.Count >= MinSamples && out.Failure.Count >= MinSamples && out.Conclusion != "Not enough data"
}

func failedDetails(out Outcome) []string {
	var details []string
	for _, check := range out.Assertions {
		if !check.Pass {
			details = append(details, check.Detail)
		}
	}
	return details
}

func recoveryTrend(points []HistoryRun) string {
	if len(points) < 2 {
		return "trend is not established"
	}
	prev := points[len(points)-2].RecoveryMs
	last := points[len(points)-1].RecoveryMs
	if prev == nil || last == nil {
		return "trend is not established"
	}
	if *last >= *prev+2000 {
		return fmt.Sprintf("last recovery is %.1fs slower than the previous judged run", float64(*last-*prev)/1000)
	}
	return "last recovery is not 2s slower than the previous judged run"
}

// FormatHistory renders the history for the CLI.
func FormatHistory(items []NameHistory) string {
	if len(items) == 0 {
		return "no completed runs\n"
	}
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s\n", item.Name)
		if len(item.KnownBad) > 0 && item.Judged == 0 {
			fmt.Fprintf(&b, "known bad, not in the pass rate\n")
		} else {
			fmt.Fprintf(&b, "pass rate %s\n", item.PassText)
		}
		if len(item.KnownBad) > 0 && item.Judged > 0 {
			fmt.Fprintf(&b, "known bad, not in the pass rate\n")
		}
		for _, point := range item.Recovery {
			fmt.Fprintf(&b, "recovery #%03d %s\n", point.ID, formatRecovery(point.RecoveryMs))
		}
		fmt.Fprintf(&b, "%s\n", item.Trend)
		for _, fail := range item.Failed {
			writeDetails(&b, fail)
		}
		for _, bad := range item.KnownBad {
			writeDetails(&b, bad)
		}
	}
	return b.String()
}

func writeDetails(b *strings.Builder, run HistoryRun) {
	if len(run.Details) == 0 {
		fmt.Fprintf(b, "#%03d assertion failed\n", run.ID)
		return
	}
	for _, detail := range run.Details {
		fmt.Fprintf(b, "#%03d %s\n", run.ID, detail)
	}
}

func formatRecovery(ms *int64) string {
	if ms == nil {
		return "not observed"
	}
	return fmt.Sprintf("%.1fs", float64(*ms)/1000)
}
