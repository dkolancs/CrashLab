package analysis

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/dkolancs/crashlab/internal/model"
)

// Window is a latency and error summary for one phase.
// MinSamples is the smallest baseline and failure sample count that can judge a fault.
const MinSamples = 10

type Window struct {
	P50          float64 `json:"p50"`
	P95          float64 `json:"p95"`
	P50All       float64 `json:"p50All"`
	P95All       float64 `json:"p95All"`
	ErrorRate    float64 `json:"errorRate"`
	Count        int     `json:"count"`
	SuccessCount int     `json:"successCount"`
}

// Hop is one service in a timestamp-correlated propagation chain.
type Hop struct {
	Service string    `json:"service"`
	At      time.Time `json:"at"`
	Reason  string    `json:"reason"`
}

// Bucket is one second of user-facing latency for the chart.
type Bucket struct {
	At           time.Time `json:"t"`
	P95          float64   `json:"p95"`
	ErrorRate    float64   `json:"errorRate"`
	SuccessCount int       `json:"successCount"`
}

// Outcome is the measured result of an experiment.
type Outcome struct {
	Baseline      Window            `json:"baseline"`
	Failure       Window            `json:"failure"`
	Recovery      Window            `json:"recovery"`
	RecoveryMs    *int64            `json:"recoveryMs"`
	RecoveredAt   time.Time         `json:"recoveredAt,omitempty"`
	Degraded      bool              `json:"degraded"`
	Conclusion    string            `json:"conclusion"`
	Hops          []Hop             `json:"propagation"`
	Buckets       []Bucket          `json:"buckets"`
	NodeStates    map[string]string `json:"nodeStates"`
	RetryFactor   float64           `json:"retryFactor"`
	CPUBaseline   float64           `json:"cpuBaseline"`
	CPUFailure    float64           `json:"cpuFailure"`
	PathNote      string            `json:"pathNote"`
	Explanation   string            `json:"explanation"`
	Assertions    []AssertionResult `json:"assertions"`
	OfferedRate   float64           `json:"offeredRate"`
	CompletedRate float64           `json:"completedRate"`
	Derived       []model.Event     `json:"-"`
}

// Comparison is the measured difference between two saved runs.
type Comparison struct {
	SameName        bool    `json:"sameName"`
	LeftName        string  `json:"leftName"`
	RightName       string  `json:"rightName"`
	ErrorRateDelta  float64 `json:"errorRateDelta"`
	RecoveryDeltaMs *int64  `json:"recoveryDeltaMs"`
	LeftRecoveryMs  *int64  `json:"leftRecoveryMs"`
	RightRecoveryMs *int64  `json:"rightRecoveryMs"`
}

type request struct {
	at    time.Time
	phase string
	lat   float64
	err   bool
}

// Evaluate compares baseline, failure, and recovery for one experiment.
// Propagation is timestamp correlation along declared dependencies, not proof of causation.
func Evaluate(exp model.Experiment, samples []model.Sample, services []model.Service, injectedAt, removedAt time.Time) Outcome {
	services = mergeTraceDependencies(services, samples)
	reqs := dropBaselineWarmup(pairRequests(samples, exp.EntryService, model.SourceLoad), 2*time.Second)
	out := Outcome{
		PathNote:   pathNote(samples),
		Baseline:   windowFor(reqs, model.PhaseBaseline),
		Failure:    windowFor(reqs, model.PhaseObserve),
		Recovery:   windowFor(reqs, model.PhaseRecovery),
		Buckets:    buckets(reqs),
		NodeStates: map[string]string{},
	}
	out.Hops = Propagation(samples, services, exp.Target, injectedAt)
	for _, hop := range out.Hops {
		if hop.Service == exp.Target {
			out.NodeStates[hop.Service] = "fault"
		} else {
			out.NodeStates[hop.Service] = "degraded"
		}
	}
	enough := out.Baseline.Count >= MinSamples && out.Failure.Count >= MinSamples
	if enough {
		if dur, at, ok := recoveryTime(reqs, removedAt, out.Baseline); ok {
			ms := dur.Milliseconds()
			out.RecoveryMs = &ms
			out.RecoveredAt = at
		}
	}
	out.RetryFactor, _, _ = retryFactor(samples, exp.Target, injectedAt, removedAt)
	out.CPUBaseline = meanMetric(samples, exp.Target, model.SourceDocker, model.MetricCPU, model.PhaseBaseline)
	out.CPUFailure = meanMetric(samples, exp.Target, model.SourceDocker, model.MetricCPU, model.PhaseObserve)

	latencyUp := enough && out.Baseline.SuccessCount > 0 && out.Failure.SuccessCount > 0 &&
		out.Failure.P95 >= out.Baseline.P95*2 && out.Failure.P95 > out.Baseline.P95+20
	errorsUp := enough && out.Failure.ErrorRate >= out.Baseline.ErrorRate+0.05
	out.Degraded = latencyUp || errorsUp
	switch {
	case !enough:
		out.Conclusion = "Not enough data"
	case out.Degraded && out.RecoveryMs != nil:
		out.Conclusion = "System degraded significantly."
	case out.Degraded:
		out.Conclusion = "System degraded and did not return to baseline."
	default:
		out.Conclusion = "System tolerated the fault."
	}
	out.OfferedRate = gaugeRate(samples, model.MetricOffered, model.PhaseBaseline)
	out.CompletedRate = gaugeRate(samples, model.MetricCompleted, model.PhaseBaseline)
	out.Derived = derivedEvents(exp, out, samples, injectedAt)
	return out
}

// Compare reports how the later run differs from the earlier one.
func Compare(leftName string, left Outcome, rightName string, right Outcome) Comparison {
	delta := Comparison{
		SameName:        leftName != "" && leftName == rightName,
		LeftName:        leftName,
		RightName:       rightName,
		ErrorRateDelta:  right.Failure.ErrorRate - left.Failure.ErrorRate,
		LeftRecoveryMs:  left.RecoveryMs,
		RightRecoveryMs: right.RecoveryMs,
	}
	if left.RecoveryMs != nil && right.RecoveryMs != nil {
		ms := *right.RecoveryMs - *left.RecoveryMs
		delta.RecoveryDeltaMs = &ms
	}
	return delta
}

func dropBaselineWarmup(reqs []request, warmup time.Duration) []request {
	var first time.Time
	has := false
	for _, req := range reqs {
		if req.phase != model.PhaseBaseline {
			continue
		}
		if !has || req.at.Before(first) {
			first = req.at
			has = true
		}
	}
	if !has {
		return reqs
	}
	cutoff := first.Add(warmup)
	filtered := make([]request, 0, len(reqs))
	for _, req := range reqs {
		if req.phase == model.PhaseBaseline && req.at.Before(cutoff) {
			continue
		}
		filtered = append(filtered, req)
	}
	if len(filterPhase(filtered, model.PhaseBaseline)) < MinSamples {
		return reqs
	}
	return filtered
}

func filterPhase(reqs []request, phase string) []request {
	var out []request
	for _, req := range reqs {
		if req.phase == phase {
			out = append(out, req)
		}
	}
	return out
}

func gaugeRate(samples []model.Sample, name, phase string) float64 {
	var pts []counterPoint
	for _, sm := range samples {
		if sm.Name == name && sm.Phase == phase {
			pts = append(pts, counterPoint{at: sm.At, v: sm.Value})
		}
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].at.Before(pts[j].at) })
	return pointRate(pts)
}

// Propagation orders the fault target and dependent services by the first
// sustained departure from each service's own baseline. Ties break toward the
// service closer to the fault. Services outside the dependency cone are ignored.
func Propagation(samples []model.Sample, services []model.Service, fault string, injectedAt time.Time) []Hop {
	dist := distances(services, fault)
	hops := []Hop{{Service: fault, At: injectedAt, Reason: "fault"}}
	for _, svc := range services {
		if svc.Name == fault {
			continue
		}
		if _, ok := dist[svc.Name]; !ok {
			continue
		}
		at, reason, ok := departure(samples, svc.Name, injectedAt)
		if !ok {
			continue
		}
		hops = append(hops, Hop{Service: svc.Name, At: at, Reason: reason})
	}
	sort.SliceStable(hops, func(i, j int) bool {
		if !hops[i].At.Equal(hops[j].At) {
			return hops[i].At.Before(hops[j].At)
		}
		return dist[hops[i].Service] < dist[hops[j].Service]
	})
	return hops
}

func distances(services []model.Service, fault string) map[string]int {
	rev := map[string][]string{}
	for _, svc := range services {
		for _, dep := range svc.DependsOn {
			rev[dep] = append(rev[dep], svc.Name)
		}
	}
	dist := map[string]int{fault: 0}
	queue := []string{fault}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range rev[cur] {
			if _, seen := dist[next]; seen {
				continue
			}
			dist[next] = dist[cur] + 1
			queue = append(queue, next)
		}
	}
	return dist
}

func departure(samples []model.Sample, service string, injectedAt time.Time) (time.Time, string, bool) {
	latAt, latOK := latencyDeparture(samples, service, injectedAt)
	errAt, errOK := errorDeparture(samples, service, injectedAt)
	switch {
	case latOK && errOK && errAt.Before(latAt):
		return errAt, "errors", true
	case latOK:
		return latAt, "latency", true
	case errOK:
		return errAt, "errors", true
	default:
		return time.Time{}, "", false
	}
}

func latencyDeparture(samples []model.Sample, service string, injectedAt time.Time) (time.Time, bool) {
	var base []float64
	var after []struct {
		at time.Time
		v  float64
	}
	for _, sm := range samples {
		if sm.Service != service || sm.Name != model.MetricLatencyMS || sm.Source != model.SourceStats {
			continue
		}
		if sm.Phase == model.PhaseBaseline || sm.At.Before(injectedAt) && sm.Phase == model.PhaseBaseline {
			base = append(base, sm.Value)
			continue
		}
		if !sm.At.Before(injectedAt) {
			after = append(after, struct {
				at time.Time
				v  float64
			}{sm.At, sm.Value})
		}
	}
	if len(base) < 3 || len(after) == 0 {
		return time.Time{}, false
	}
	mean, std := meanStd(base)
	threshold := math.Max(mean*2, mean+3*std)
	threshold = math.Max(threshold, mean+15)
	sort.Slice(after, func(i, j int) bool { return after[i].at.Before(after[j].at) })
	for i := range after {
		if after[i].v <= threshold {
			continue
		}
		for j := i + 1; j < len(after); j++ {
			if after[j].at.Sub(after[i].at) > 3*time.Second {
				break
			}
			if after[j].v > threshold {
				return after[i].at, true
			}
		}
	}
	return time.Time{}, false
}

func errorDeparture(samples []model.Sample, service string, injectedAt time.Time) (time.Time, bool) {
	type point struct {
		at   time.Time
		req  float64
		err  float64
		hasR bool
		hasE bool
	}
	by := map[int64]*point{}
	var keys []int64
	for _, sm := range samples {
		if sm.Service != service || sm.Source != model.SourceStats {
			continue
		}
		if sm.Name != model.MetricRequests && sm.Name != model.MetricErrors {
			continue
		}
		key := sm.At.UnixNano()
		item, ok := by[key]
		if !ok {
			item = &point{at: sm.At}
			by[key] = item
			keys = append(keys, key)
		}
		if sm.Name == model.MetricRequests {
			item.req = sm.Value
			item.hasR = true
		} else {
			item.err = sm.Value
			item.hasE = true
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var series []point
	for _, key := range keys {
		item := by[key]
		if item.hasR && item.hasE {
			series = append(series, *item)
		}
	}
	if len(series) < 4 {
		return time.Time{}, false
	}
	var baseErr, baseReq float64
	var after []point
	prev := series[0]
	for _, item := range series[1:] {
		dReq := item.req - prev.req
		dErr := item.err - prev.err
		if dReq < 0 || dErr < 0 {
			prev = item
			continue
		}
		if item.at.Before(injectedAt) {
			baseReq += dReq
			baseErr += dErr
		} else if dReq > 0 {
			after = append(after, point{at: item.at, req: dReq, err: dErr})
		}
		prev = item
	}
	baseRate := 0.0
	if baseReq > 0 {
		baseRate = baseErr / baseReq
	}
	threshold := baseRate + 0.2
	for i := range after {
		if after[i].req <= 0 || after[i].err/after[i].req <= threshold {
			continue
		}
		for j := i + 1; j < len(after); j++ {
			if after[j].at.Sub(after[i].at) > 3*time.Second {
				break
			}
			if after[j].req > 0 && after[j].err/after[j].req > threshold {
				return after[i].at, true
			}
		}
	}
	return time.Time{}, false
}

func derivedEvents(exp model.Experiment, out Outcome, samples []model.Sample, injectedAt time.Time) []model.Event {
	var events []model.Event
	for _, hop := range out.Hops {
		if hop.Reason != "latency" && hop.Reason != "errors" {
			continue
		}
		label := "latency"
		if hop.Reason == "errors" {
			label = "errors"
		}
		events = append(events, model.Event{
			ExperimentID: exp.ID,
			At:           hop.At,
			Kind:         model.KindDegraded,
			Service:      hop.Service,
			Message:      hop.Service + " " + label + " departed from baseline",
		})
	}
	if out.Failure.ErrorRate >= out.Baseline.ErrorRate+0.05 {
		if at, ok := firstLoadError(samples, exp.EntryService, injectedAt); ok {
			events = append(events, model.Event{
				ExperimentID: exp.ID,
				At:           at,
				Kind:         model.KindTimeouts,
				Service:      exp.EntryService,
				Message:      "Request timeouts begin",
			})
		}
	}
	if factor, at, ok := retryFactor(samples, exp.Target, injectedAt, time.Time{}); ok && factor >= 2 {
		events = append(events, model.Event{
			ExperimentID: exp.ID,
			At:           at,
			Kind:         model.KindRetry,
			Service:      exp.Target,
			Message:      "Retry volume increased " + formatFactor(factor),
		})
	}
	if out.RecoveryMs != nil {
		events = append(events, model.Event{
			ExperimentID: exp.ID,
			At:           out.RecoveredAt,
			Kind:         model.KindRecovered,
			Service:      exp.EntryService,
			Message:      "System recovered",
		})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
	return events
}

func firstLoadError(samples []model.Sample, service string, injectedAt time.Time) (time.Time, bool) {
	var at time.Time
	found := false
	for _, sm := range samples {
		if sm.Source != model.SourceLoad || sm.Service != service || sm.Name != model.MetricError || sm.Value < 1 {
			continue
		}
		if sm.At.Before(injectedAt) {
			continue
		}
		if !found || sm.At.Before(at) {
			at = sm.At
			found = true
		}
	}
	return at, found
}

func retryFactor(samples []model.Sample, target string, injectedAt, removedAt time.Time) (float64, time.Time, bool) {
	pts := counterSeries(samples, target, model.MetricRxPackets)
	if len(pts) < 4 {
		return 0, time.Time{}, false
	}
	baseRate := seriesRate(pts, func(at time.Time) bool { return at.Before(injectedAt) })
	if baseRate < 1 {
		return 0, time.Time{}, false
	}
	var faultPts []counterPoint
	for _, p := range pts {
		if p.at.Before(injectedAt) {
			continue
		}
		if !removedAt.IsZero() && !p.at.Before(removedAt) {
			continue
		}
		faultPts = append(faultPts, p)
	}
	faultRate := pointRate(faultPts)
	if faultRate < 2*baseRate {
		return faultRate / baseRate, time.Time{}, false
	}
	at := injectedAt
	for i := 1; i < len(pts); i++ {
		if pts[i].at.Before(injectedAt) {
			continue
		}
		dt := pts[i].at.Sub(pts[i-1].at).Seconds()
		if dt <= 0 {
			continue
		}
		rate := (pts[i].v - pts[i-1].v) / dt
		if rate >= 2*baseRate {
			at = pts[i].at
			break
		}
	}
	return faultRate / baseRate, at, true
}

type counterPoint struct {
	at time.Time
	v  float64
}

func counterSeries(samples []model.Sample, service, name string) []counterPoint {
	var pts []counterPoint
	for _, sm := range samples {
		if sm.Service == service && sm.Name == name && sm.Source == model.SourceDocker {
			pts = append(pts, counterPoint{at: sm.At, v: sm.Value})
		}
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].at.Before(pts[j].at) })
	return pts
}

func seriesRate(pts []counterPoint, keep func(time.Time) bool) float64 {
	var filtered []counterPoint
	for _, p := range pts {
		if keep(p.at) {
			filtered = append(filtered, p)
		}
	}
	return pointRate(filtered)
}

func pointRate(pts []counterPoint) float64 {
	if len(pts) < 2 {
		return 0
	}
	var delta, dt float64
	for i := 1; i < len(pts); i++ {
		step := pts[i].at.Sub(pts[i-1].at).Seconds()
		diff := pts[i].v - pts[i-1].v
		if step <= 0 || diff < 0 {
			continue
		}
		delta += diff
		dt += step
	}
	if dt <= 0 || delta <= 0 {
		return 0
	}
	return delta / dt
}

func recoveryTime(reqs []request, removedAt time.Time, baseline Window) (time.Duration, time.Time, bool) {
	if removedAt.IsZero() || baseline.Count < MinSamples || baseline.SuccessCount == 0 {
		return 0, time.Time{}, false
	}
	threshold := math.Max(baseline.P95*1.5, baseline.P95+40)
	var after []request
	for _, r := range reqs {
		if !r.at.Before(removedAt) {
			after = append(after, r)
		}
	}
	sort.Slice(after, func(i, j int) bool { return after[i].at.Before(after[j].at) })
	healthy := func(r request) bool {
		return !r.err && r.lat <= threshold
	}
	for i := range after {
		if !healthy(after[i]) {
			continue
		}
		end := after[i].at.Add(3 * time.Second)
		n := 0
		ok := true
		for j := i; j < len(after) && after[j].at.Before(end); j++ {
			n++
			if !healthy(after[j]) {
				ok = false
				break
			}
		}
		if ok && n >= 2 {
			return after[i].at.Sub(removedAt), after[i].at, true
		}
	}
	return 0, time.Time{}, false
}

func pairRequests(samples []model.Sample, service, source string) []request {
	type acc struct {
		at    time.Time
		phase string
		lat   float64
		err   float64
		hasL  bool
		hasE  bool
	}
	byKey := map[int64]*acc{}
	var keys []int64
	for _, sm := range samples {
		if sm.Service != service || sm.Source != source {
			continue
		}
		if sm.Name != model.MetricLatencyMS && sm.Name != model.MetricError {
			continue
		}
		key := sm.At.UnixNano()
		item, ok := byKey[key]
		if !ok {
			item = &acc{at: sm.At, phase: sm.Phase}
			byKey[key] = item
			keys = append(keys, key)
		}
		if sm.Name == model.MetricLatencyMS {
			item.lat = sm.Value
			item.hasL = true
		} else {
			item.err = sm.Value
			item.hasE = true
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]request, 0, len(keys))
	for _, key := range keys {
		item := byKey[key]
		if !item.hasL {
			continue
		}
		out = append(out, request{at: item.at, phase: item.phase, lat: item.lat, err: item.hasE && item.err >= 1})
	}
	return out
}

func windowFor(reqs []request, phase string) Window {
	var subset []request
	for _, r := range reqs {
		if r.phase == phase {
			subset = append(subset, r)
		}
	}
	return summarize(subset)
}

func summarize(reqs []request) Window {
	if len(reqs) == 0 {
		return Window{}
	}
	all := make([]float64, len(reqs))
	var success []float64
	errs := 0
	for i, r := range reqs {
		all[i] = r.lat
		if r.err {
			errs++
			continue
		}
		success = append(success, r.lat)
	}
	sort.Float64s(all)
	sort.Float64s(success)
	return Window{
		P50:          percentile(success, 0.50),
		P95:          percentile(success, 0.95),
		P50All:       percentile(all, 0.50),
		P95All:       percentile(all, 0.95),
		ErrorRate:    float64(errs) / float64(len(reqs)),
		Count:        len(reqs),
		SuccessCount: len(success),
	}
}

func buckets(reqs []request) []Bucket {
	if len(reqs) == 0 {
		return nil
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].at.Before(reqs[j].at) })
	start := reqs[0].at.Truncate(time.Second)
	end := reqs[len(reqs)-1].at
	var out []Bucket
	for t := start; !t.After(end); t = t.Add(time.Second) {
		var subset []request
		next := t.Add(time.Second)
		for _, r := range reqs {
			if !r.at.Before(t) && r.at.Before(next) {
				subset = append(subset, r)
			}
		}
		if len(subset) == 0 {
			continue
		}
		w := summarize(subset)
		out = append(out, Bucket{At: t, P95: w.P95, ErrorRate: w.ErrorRate})
	}
	return out
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func meanStd(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range xs {
		sum += v
	}
	mean := sum / float64(len(xs))
	if len(xs) < 2 {
		return mean, 0
	}
	var ss float64
	for _, v := range xs {
		d := v - mean
		ss += d * d
	}
	return mean, math.Sqrt(ss / float64(len(xs)-1))
}

func meanMetric(samples []model.Sample, service, source, name, phase string) float64 {
	var sum float64
	n := 0
	for _, sm := range samples {
		if sm.Service == service && sm.Source == source && sm.Name == name && sm.Phase == phase && sm.Value > 0 {
			sum += sm.Value
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func formatFactor(v float64) string {
	return fmt.Sprintf("%.1fx", v)
}
