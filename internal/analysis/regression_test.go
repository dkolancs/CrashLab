package analysis

import (
	"strings"
	"testing"
	"time"

	"github.com/dkolancs/crashlab/internal/model"
)

func TestRegressionPrintsHigherErrorRate(t *testing.T) {
	prevMs := int64(200)
	curMs := int64(400)
	previous := Outcome{
		Baseline:   Window{Count: 10, SuccessCount: 10},
		Failure:    Window{ErrorRate: 0.01, Count: 10, SuccessCount: 10},
		RecoveryMs: &prevMs,
		Assertions: []AssertionResult{{Name: "error_rate", Pass: true, Detail: "error rate 1%"}},
		Conclusion: "System tolerated the fault.",
	}
	current := Outcome{
		Baseline:   Window{Count: 10, SuccessCount: 10},
		Failure:    Window{ErrorRate: 0.80, Count: 10, SuccessCount: 2},
		RecoveryMs: &curMs,
		Assertions: []AssertionResult{{Name: "error_rate", Pass: false, Detail: "error rate 80%"}},
	}
	note := RegressionNote(7, previous, current)
	if !strings.Contains(note, "Regression against passing run #007") || !strings.Contains(note, "80.0%") || !strings.Contains(note, "not a cause") {
		t.Fatalf("note = %s", note)
	}
	if strings.Contains(note, "caused") {
		t.Fatalf("note assigns a cause: %s", note)
	}
	current.Regression = note
	text := Format(model.Experiment{ID: 8, Name: "payment-latency", Target: "payment-service", FaultType: model.FaultLatency, Delay: 800 * time.Millisecond, Duration: 30 * time.Second, StartedAt: time.Now()}, current, nil)
	if !strings.Contains(text, "REGRESSION") || !strings.Contains(text, note) {
		t.Fatalf("report missing regression\n%s", text)
	}
}

func TestRegressionStaysQuietBelowThreshold(t *testing.T) {
	prevMs := int64(200)
	curMs := int64(900)
	previous := Outcome{Baseline: Window{Count: 10, SuccessCount: 10}, Failure: Window{ErrorRate: 0.01, Count: 10, SuccessCount: 10}, RecoveryMs: &prevMs}
	current := Outcome{Baseline: Window{Count: 10, SuccessCount: 10}, Failure: Window{ErrorRate: 0.04, Count: 10, SuccessCount: 10}, RecoveryMs: &curMs}
	if note := RegressionNote(1, previous, current); note != "" {
		t.Fatalf("note = %s", note)
	}
}

func TestTraceOutsideFaultWindowDoesNotAddEdge(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	injected := start.Add(10 * time.Second)
	removed := start.Add(20 * time.Second)
	services := []model.Service{{Name: "payment-service"}}
	var samples []model.Sample
	for sec := 0; sec < 20; sec++ {
		phase := model.PhaseBaseline
		lat := 40.0
		if sec >= 10 {
			phase = model.PhaseObserve
		}
		if sec >= 12 {
			lat = 900
		}
		samples = append(samples, model.Sample{
			At: start.Add(time.Duration(sec) * time.Second), Phase: phase, Source: model.SourceStats,
			Service: "checkout-service", Name: model.MetricLatencyMS, Value: lat,
		})
	}
	early := append([]model.Sample{}, samples...)
	early = append(early, model.Sample{
		At: start.Add(2 * time.Second), Phase: model.PhaseBaseline, Source: model.SourceTrace,
		Service: "checkout-service>payment-service", Name: model.MetricTrace, Value: 10,
	})
	earlyHops := Propagation(filterTraceWindow(early, injected, removed), services, "payment-service", injected)
	if len(earlyHops) != 1 || earlyHops[0].Service != "payment-service" {
		t.Fatalf("early hops = %#v", earlyHops)
	}
	during := append([]model.Sample{}, samples...)
	during = append(during, model.Sample{
		At: injected.Add(time.Second), Phase: model.PhaseObserve, Source: model.SourceTrace,
		Service: "checkout-service>payment-service", Name: model.MetricTrace, Value: 10,
	})
	hops := Propagation(filterTraceWindow(during, injected, removed), mergeTraceDependencies(services, filterTraceWindow(during, injected, removed)), "payment-service", injected)
	if len(hops) != 2 || hops[1].Service != "checkout-service" {
		t.Fatalf("hops = %#v", hops)
	}
}
