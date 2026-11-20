package analysis

import (
	"strings"
	"testing"

	"github.com/dkolancs/crashlab/internal/model"
)

func TestParseAndJudgeAssertions(t *testing.T) {
	rate, err := ParseAssertion("error_rate", "< 5%")
	if err != nil || rate.Limit != 0.05 {
		t.Fatalf("rate = %#v %v", rate, err)
	}
	p95, err := ParseAssertion("p95_latency", "< 1000ms")
	if err != nil || p95.Limit != 1000 {
		t.Fatalf("p95 = %#v %v", p95, err)
	}
	recovery, err := ParseAssertion("recovery_time", "< 10s")
	if err != nil || recovery.Limit != 10000 {
		t.Fatalf("recovery = %#v %v", recovery, err)
	}
	ms := int64(4000)
	out := Outcome{
		Failure:    Window{P95: 1600, ErrorRate: 1},
		RecoveryMs: &ms,
	}
	results := Judge(out, []Assertion{rate, p95, recovery})
	if len(results) != 3 || results[0].Pass || results[1].Pass || !results[2].Pass {
		t.Fatalf("results = %#v", results)
	}
	if !Failed(results) {
		t.Fatal("expected a failed assertion set")
	}
}

func TestExplainCitesNumbers(t *testing.T) {
	ms := int64(300)
	text := Explain(model.Experiment{Target: "payment-service", FaultType: model.FaultLatency, Delay: 800 * 1000000}, Outcome{
		Baseline:    Window{P95: 33, ErrorRate: 0, Count: 10},
		Failure:     Window{P95: 1600, ErrorRate: 1, Count: 10},
		Degraded:    true,
		RecoveryMs:  &ms,
		RetryFactor: 4,
		Hops:        []Hop{{Service: "payment-service"}, {Service: "checkout-service"}, {Service: "storefront"}},
	})
	for _, part := range []string{"checkout-service", "100.0%", "1.6 sec", "4.0x", "0.3s"} {
		if !strings.Contains(text, part) {
			t.Fatalf("explanation missing %q\n%s", part, text)
		}
	}
}
