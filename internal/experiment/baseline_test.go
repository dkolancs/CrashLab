package experiment

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/dkolancs/crashlab/internal/model"
	"github.com/dkolancs/crashlab/internal/store"
)

func TestCopyBaselineKeepsOnlyBaselineSamples(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "crashlab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	src, err := db.CreateExperiment(model.Experiment{Name: "payment-loss", Status: model.StatusCompleted, StartedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := db.CreateExperiment(model.Experiment{Name: "payment-kill", Status: model.StatusRunning, StartedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if err := db.InsertSamples([]model.Sample{
		{ExperimentID: src.ID, At: at, Phase: model.PhaseBaseline, Source: model.SourceLoad, Service: "storefront", Name: model.MetricLatencyMS, Value: 30},
		{ExperimentID: src.ID, At: at.Add(time.Second), Phase: model.PhaseObserve, Source: model.SourceLoad, Service: "storefront", Name: model.MetricLatencyMS, Value: 900},
	}); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Store: db}
	if err := r.copyBaseline(dst.ID, src.ID); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListSamples(dst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Phase != model.PhaseBaseline || got[0].Value != 30 {
		t.Fatalf("samples = %#v", got)
	}
}
