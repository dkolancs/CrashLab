package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/dkolancs/crashlab/internal/experiment"
	"github.com/dkolancs/crashlab/internal/model"
	"github.com/dkolancs/crashlab/internal/store"
)

func TestStopRunningExperiment(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "crashlab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exp, err := db.CreateExperiment(model.Experiment{
		Name: "payment-latency", Status: model.StatusRunning, Phase: model.PhaseObserve,
		Target: "payment-service", Delay: 800 * time.Millisecond, Baseline: 15 * time.Second,
		Duration: 30 * time.Second, Recovery: 15 * time.Second,
		EntryURL: "http://127.0.0.1:8081/checkout", EntryService: "storefront",
		ComposeFile: "demo/compose.yaml", StartedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &experiment.Runner{Store: db}
	srv := &Server{Store: db, Runner: runner}
	req := httptest.NewRequest(http.MethodPost, "/api/experiments/1/stop", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	srv.stop(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if exp.ID == 0 {
		t.Fatal("missing id")
	}
}
