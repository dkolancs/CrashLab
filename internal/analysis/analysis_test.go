package analysis

import (
	"strings"
	"testing"
	"time"

	"github.com/dkolancs/crashlab/internal/model"
)

func TestPercentileAndErrorRate(t *testing.T) {
	lats := make([]float64, 100)
	for i := range lats {
		lats[i] = float64(i + 1)
	}
	if got := percentile(lats, 0.50); got != 50 {
		t.Fatalf("p50 = %v", got)
	}
	if got := percentile(lats, 0.95); got != 95 {
		t.Fatalf("p95 = %v", got)
	}
	reqs := []request{
		{lat: 10},
		{lat: 20, err: true},
		{lat: 30},
		{lat: 40},
	}
	w := summarize(reqs)
	if w.ErrorRate != 0.25 || w.Count != 4 || w.SuccessCount != 3 || w.P50All != 20 || w.P50 != 30 {
		t.Fatalf("window = %#v", w)
	}
}

func TestKillDoesNotImproveSuccessLatency(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	injected := start.Add(10 * time.Second)
	removed := start.Add(20 * time.Second)
	exp := model.Experiment{
		ID: 4, Target: "payment-service", EntryService: "storefront",
		FaultType: model.FaultKill, StartedAt: start,
	}
	var samples []model.Sample
	add := func(at time.Time, phase string, lat float64, err bool) {
		ev := 0.0
		if err {
			ev = 1
		}
		samples = append(samples,
			model.Sample{At: at, Phase: phase, Source: model.SourceLoad, Service: "storefront", Name: model.MetricLatencyMS, Value: lat},
			model.Sample{At: at, Phase: phase, Source: model.SourceLoad, Service: "storefront", Name: model.MetricError, Value: ev},
		)
	}
	for i := 0; i < 10; i++ {
		add(start.Add(time.Duration(i)*time.Second), model.PhaseBaseline, 40, false)
	}
	for i := 10; i < 20; i++ {
		add(start.Add(time.Duration(i)*time.Second), model.PhaseObserve, 8, true)
	}
	out := Evaluate(exp, samples, nil, injected, removed)
	if !out.Degraded || out.Failure.ErrorRate != 1 || out.Failure.SuccessCount != 0 {
		t.Fatalf("outcome = %#v", out)
	}
	if out.Failure.P95 != 0 || out.Failure.P95All >= out.Baseline.P95 {
		t.Fatalf("failure latency success=%v all=%v baseline=%v", out.Failure.P95, out.Failure.P95All, out.Baseline.P95)
	}
	text := Format(exp, out, nil)
	if strings.Contains(text, "p95 latency      8 ms") {
		t.Fatalf("report treats fast errors as a better p95\n%s", text)
	}
	if !strings.Contains(text, "n/a") || !strings.Contains(text, "System degraded") {
		t.Fatalf("report = %s", text)
	}
}

func TestShortBaselineIsNotEnoughData(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exp := model.Experiment{ID: 1, Target: "payment-service", EntryService: "storefront", StartedAt: start}
	var samples []model.Sample
	for i := 0; i < 3; i++ {
		at := start.Add(time.Duration(i) * time.Second)
		samples = append(samples,
			model.Sample{At: at, Phase: model.PhaseBaseline, Source: model.SourceLoad, Service: "storefront", Name: model.MetricLatencyMS, Value: 30},
			model.Sample{At: at, Phase: model.PhaseBaseline, Source: model.SourceLoad, Service: "storefront", Name: model.MetricError, Value: 0},
			model.Sample{At: at.Add(10 * time.Second), Phase: model.PhaseObserve, Source: model.SourceLoad, Service: "storefront", Name: model.MetricLatencyMS, Value: 30},
			model.Sample{At: at.Add(10 * time.Second), Phase: model.PhaseObserve, Source: model.SourceLoad, Service: "storefront", Name: model.MetricError, Value: 0},
		)
	}
	out := Evaluate(exp, samples, nil, start.Add(10*time.Second), start.Add(20*time.Second))
	if out.Conclusion != "Not enough data" || out.RecoveryMs != nil {
		t.Fatalf("conclusion=%s recovery=%v", out.Conclusion, out.RecoveryMs)
	}
	if !strings.Contains(Format(exp, out, nil), "not enough data") {
		t.Fatal("report missing not enough data")
	}
}

func TestRecoveryAfterSustainedHealth(t *testing.T) {
	removed := time.Date(2026, 1, 1, 0, 0, 10, 0, time.UTC)
	baseline := Window{P50: 40, P95: 50, ErrorRate: 0, Count: 10, SuccessCount: 10}
	var reqs []request
	reqs = append(reqs, request{at: removed.Add(500 * time.Millisecond), lat: 800, err: true})
	reqs = append(reqs, request{at: removed.Add(1 * time.Second), lat: 700, err: true})
	for _, ms := range []int{4000, 4500, 5000, 6200} {
		reqs = append(reqs, request{at: removed.Add(time.Duration(ms) * time.Millisecond), lat: 45, err: false})
	}
	dur, at, ok := recoveryTime(reqs, removed, baseline)
	if !ok {
		t.Fatal("expected recovery")
	}
	if at.Sub(removed) != 4*time.Second || dur != 4*time.Second {
		t.Fatalf("recovery at %s dur %s", at, dur)
	}
}

func TestPropagationOrdersDependentsAndIgnoresUnrelated(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	injected := base.Add(10 * time.Second)
	services := []model.Service{
		{Name: "storefront", DependsOn: []string{"checkout-service", "inventory-service"}},
		{Name: "checkout-service", DependsOn: []string{"payment-service"}},
		{Name: "inventory-service", DependsOn: []string{"postgres"}},
		{Name: "payment-service"},
		{Name: "postgres"},
		{Name: "other"},
	}
	var samples []model.Sample
	add := func(svc string, sec int, lat float64) {
		phase := model.PhaseBaseline
		if sec >= 10 {
			phase = model.PhaseObserve
		}
		samples = append(samples, model.Sample{
			At:      base.Add(time.Duration(sec) * time.Second),
			Phase:   phase,
			Source:  model.SourceStats,
			Service: svc,
			Name:    model.MetricLatencyMS,
			Value:   lat,
		})
	}
	for sec := 0; sec < 20; sec++ {
		c, s := 40.0, 55.0
		if sec >= 12 {
			c = 900
		}
		if sec >= 14 {
			s = 1400
		}
		other := 10.0
		if sec >= 11 {
			other = 800
		}
		add("checkout-service", sec, c)
		add("storefront", sec, s)
		add("inventory-service", sec, 8)
		add("payment-service", sec, 20)
		add("other", sec, other)
	}
	hops := Propagation(samples, services, "payment-service", injected)
	if len(hops) != 3 {
		t.Fatalf("hops = %#v", hops)
	}
	want := []string{"payment-service", "checkout-service", "storefront"}
	for i, name := range want {
		if hops[i].Service != name {
			t.Fatalf("hop %d = %s, want %s (%#v)", i, hops[i].Service, name, hops)
		}
	}
	if !hops[1].At.Equal(base.Add(12 * time.Second)) {
		t.Fatalf("checkout at %s", hops[1].At)
	}
	if !hops[2].At.Equal(base.Add(14 * time.Second)) {
		t.Fatalf("storefront at %s", hops[2].At)
	}
}

func TestSingleSpikeDoesNotDepart(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	injected := base.Add(10 * time.Second)
	services := []model.Service{
		{Name: "checkout-service", DependsOn: []string{"payment-service"}},
		{Name: "payment-service"},
	}
	var samples []model.Sample
	for sec := 0; sec < 20; sec++ {
		lat := 40.0
		if sec == 12 {
			lat = 900
		}
		phase := model.PhaseBaseline
		if sec >= 10 {
			phase = model.PhaseObserve
		}
		samples = append(samples, model.Sample{
			At: base.Add(time.Duration(sec) * time.Second), Phase: phase, Source: model.SourceStats,
			Service: "checkout-service", Name: model.MetricLatencyMS, Value: lat,
		})
	}
	hops := Propagation(samples, services, "payment-service", injected)
	if len(hops) != 1 || hops[0].Service != "payment-service" {
		t.Fatalf("hops = %#v", hops)
	}
}

func TestTieBreaksTowardFault(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	injected := base.Add(10 * time.Second)
	services := []model.Service{
		{Name: "storefront", DependsOn: []string{"checkout-service"}},
		{Name: "checkout-service", DependsOn: []string{"payment-service"}},
		{Name: "payment-service"},
	}
	var samples []model.Sample
	for sec := 0; sec < 16; sec++ {
		c, s := 40.0, 50.0
		if sec >= 12 {
			c, s = 900, 900
		}
		phase := model.PhaseBaseline
		if sec >= 10 {
			phase = model.PhaseObserve
		}
		for _, pair := range []struct {
			name string
			lat  float64
		}{{"checkout-service", c}, {"storefront", s}} {
			samples = append(samples, model.Sample{
				At: base.Add(time.Duration(sec) * time.Second), Phase: phase, Source: model.SourceStats,
				Service: pair.name, Name: model.MetricLatencyMS, Value: pair.lat,
			})
		}
	}
	hops := Propagation(samples, services, "payment-service", injected)
	if len(hops) != 3 || hops[1].Service != "checkout-service" || hops[2].Service != "storefront" {
		t.Fatalf("hops = %#v", hops)
	}
}

func TestReportShowsDegradationAndRecovery(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	injected := start.Add(10 * time.Second)
	removed := start.Add(20 * time.Second)
	exp := model.Experiment{
		ID: 18, Name: "payment-latency", Target: "payment-service", EntryService: "storefront",
		Delay: 800 * time.Millisecond, Duration: 10 * time.Second, StartedAt: start,
	}
	services := []model.Service{
		{Name: "storefront", DependsOn: []string{"checkout-service"}},
		{Name: "checkout-service", DependsOn: []string{"payment-service"}},
		{Name: "payment-service"},
	}
	var samples []model.Sample
	addLoad := func(at time.Time, phase string, lat float64, err bool) {
		ev := 0.0
		if err {
			ev = 1
		}
		samples = append(samples,
			model.Sample{At: at, Phase: phase, Source: model.SourceLoad, Service: "storefront", Name: model.MetricLatencyMS, Value: lat},
			model.Sample{At: at, Phase: phase, Source: model.SourceLoad, Service: "storefront", Name: model.MetricError, Value: ev},
		)
	}
	for i := 0; i < 10; i++ {
		at := start.Add(time.Duration(i) * time.Second)
		addLoad(at, model.PhaseBaseline, 80, false)
		samples = append(samples, model.Sample{
			At: at, Phase: model.PhaseBaseline, Source: model.SourceStats, Service: "checkout-service",
			Name: model.MetricLatencyMS, Value: 40,
		})
		samples = append(samples, model.Sample{
			At: at, Phase: model.PhaseBaseline, Source: model.SourceDocker, Service: "payment-service",
			Name: model.MetricRxPackets, Value: float64(i * 100),
		})
	}
	for i := 10; i < 20; i++ {
		at := start.Add(time.Duration(i) * time.Second)
		addLoad(at, model.PhaseObserve, 1600, true)
		lat := 40.0
		if i >= 12 {
			lat = 900
		}
		samples = append(samples, model.Sample{
			At: at, Phase: model.PhaseObserve, Source: model.SourceStats, Service: "checkout-service",
			Name: model.MetricLatencyMS, Value: lat,
		})
		samples = append(samples, model.Sample{
			At: at, Phase: model.PhaseObserve, Source: model.SourceDocker, Service: "payment-service",
			Name: model.MetricRxPackets, Value: 1000 + float64(i-10)*400,
		})
	}
	for i := 20; i < 28; i++ {
		at := start.Add(time.Duration(i) * time.Second)
		addLoad(at, model.PhaseRecovery, 70, false)
	}
	out := Evaluate(exp, samples, services, injected, removed)
	if !out.Degraded {
		t.Fatal("expected degraded")
	}
	if out.RecoveryMs == nil || *out.RecoveryMs > 2000 {
		t.Fatalf("recovery = %v", out.RecoveryMs)
	}
	if out.RetryFactor < 2 {
		t.Fatalf("retry factor = %v", out.RetryFactor)
	}
	if out.Conclusion != "System degraded significantly." {
		t.Fatalf("conclusion = %s", out.Conclusion)
	}
	events := []model.Event{
		{At: injected, Kind: model.KindFaultInjected, Service: "payment-service", Message: "Fault injected"},
		{At: removed, Kind: model.KindFaultRemoved, Service: "payment-service", Message: "Fault removed"},
	}
	events = append(events, out.Derived...)
	text := Format(exp, out, events)
	for _, part := range []string{
		"CRASHLAB EXPERIMENT #018",
		"payment-service",
		"+800ms network latency",
		"BASELINE",
		"DURING FAILURE",
		"RECOVERY",
		"OBSERVED PROPAGATION",
		"checkout-service",
		"Request timeouts begin",
		"System degraded significantly.",
		"Recovery time:",
	} {
		if !strings.Contains(text, part) {
			t.Fatalf("report missing %q\n%s", part, text)
		}
	}
}
