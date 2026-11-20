package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/dkolancs/crashlab/internal/model"
)

func TestRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "crashlab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	started := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	exp, err := s.CreateExperiment(model.Experiment{
		Name:         "payment-latency",
		Status:       model.StatusRunning,
		Phase:        model.PhaseBaseline,
		Target:       "payment-service",
		Delay:        800 * time.Millisecond,
		Baseline:     15 * time.Second,
		Duration:     30 * time.Second,
		Recovery:     15 * time.Second,
		EntryURL:     "http://127.0.0.1:8081/checkout",
		EntryService: "storefront",
		ComposeFile:  "demo/compose.yaml",
		Project:      "crashlab-demo",
		StartedAt:    started,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exp.ID == 0 {
		t.Fatal("expected id")
	}

	err = s.SaveServices(exp.ID, []model.Service{{
		Name:         "storefront",
		ContainerID:  "abc",
		Image:        "storefront",
		Ports:        []string{"8081->8080/tcp"},
		HostPorts:    []int{8081},
		Networks:     []string{"lab"},
		DependsOn:    []string{"checkout-service"},
		Health:       "running",
		Status:       "running",
		RestartCount: 0,
	}})
	if err != nil {
		t.Fatal(err)
	}
	svcs, err := s.ListServices(exp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 || svcs[0].HostPorts[0] != 8081 || svcs[0].DependsOn[0] != "checkout-service" {
		t.Fatalf("services = %#v", svcs)
	}

	at := started.Add(time.Second)
	if err := s.InsertSamples([]model.Sample{{
		ExperimentID: exp.ID,
		At:           at,
		Phase:        model.PhaseBaseline,
		Source:       model.SourceLoad,
		Service:      "storefront",
		Name:         model.MetricLatencyMS,
		Value:        42,
	}}); err != nil {
		t.Fatal(err)
	}
	samples, err := s.ListSamples(exp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].Value != 42 {
		t.Fatalf("samples = %#v", samples)
	}

	if err := s.AddEvent(model.Event{
		ExperimentID: exp.ID,
		At:           at,
		Kind:         model.KindFaultInjected,
		Service:      "payment-service",
		Message:      "Fault injected",
	}); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListEvents(exp.ID)
	if err != nil || len(events) != 1 || events[0].Message != "Fault injected" {
		t.Fatalf("events err=%v %#v", err, events)
	}

	if err := s.SaveResult(exp.ID, "REPORT", `{"degraded":true}`); err != nil {
		t.Fatal(err)
	}
	text, summary, ok, err := s.GetResult(exp.ID)
	if err != nil || !ok || text != "REPORT" || summary != `{"degraded":true}` {
		t.Fatalf("result ok=%v err=%v %q %q", ok, err, text, summary)
	}

	got, err := s.GetExperiment(exp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Delay != 800*time.Millisecond || got.EntryService != "storefront" {
		t.Fatalf("got %#v", got)
	}
}
