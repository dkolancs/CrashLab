package api

import (
	"encoding/json"
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

func TestCompareReportsErrorAndRecoveryDelta(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "crashlab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mk := func(summary string) int64 {
		t.Helper()
		exp, err := db.CreateExperiment(model.Experiment{
			Name: "payment-latency", Status: model.StatusCompleted, Phase: model.PhaseDone,
			Target: "payment-service", StartedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SaveResult(exp.ID, "report", summary); err != nil {
			t.Fatal(err)
		}
		return exp.ID
	}
	left := mk(`{"baseline":{"p95":40,"errorRate":0,"count":10,"successCount":10},"failure":{"p95":0,"errorRate":0.1,"count":10,"successCount":0},"recovery":{"p95":40,"errorRate":0,"count":10,"successCount":10},"recoveryMs":200,"assertions":[{"name":"error_rate","pass":true,"detail":"error rate 10%"}],"propagation":[{"service":"payment-service","at":"2026-01-01T00:00:00Z","reason":"latency"}]}`)
	right := mk(`{"baseline":{"p95":40,"errorRate":0,"count":10,"successCount":10},"failure":{"p95":0,"errorRate":1,"count":10,"successCount":0},"recovery":{"p95":40,"errorRate":0,"count":10,"successCount":10},"recoveryMs":2200,"assertions":[{"name":"error_rate","pass":false,"detail":"error rate 100%"}],"propagation":[{"service":"payment-service","at":"2026-01-01T00:00:00Z","reason":"latency"},{"service":"storefront","at":"2026-01-01T00:00:01Z","reason":"error"}]}`)
	srv := &Server{Store: db, Runner: &experiment.Runner{Store: db}}
	req := httptest.NewRequest(http.MethodGet, "/api/compare?left=1&right=2", nil)
	rec := httptest.NewRecorder()
	srv.compare(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Delta struct {
			SameName        bool    `json:"sameName"`
			ErrorRateDelta  float64 `json:"errorRateDelta"`
			RecoveryDeltaMs *int64  `json:"recoveryDeltaMs"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Delta.SameName || body.Delta.ErrorRateDelta != 0.9 || body.Delta.RecoveryDeltaMs == nil || *body.Delta.RecoveryDeltaMs != 2000 {
		t.Fatalf("delta = %#v ids %d %d", body.Delta, left, right)
	}
}

func TestHistoryEndpointSeparatesKnownBad(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "crashlab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	save := func(name string, expect bool, summary string) {
		t.Helper()
		exp, err := db.CreateExperiment(model.Experiment{
			Name: name, Status: model.StatusCompleted, Phase: model.PhaseDone,
			ExpectFailure: expect, StartedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SaveResult(exp.ID, "report", summary); err != nil {
			t.Fatal(err)
		}
	}
	save("ci-smoke", false, `{"baseline":{"count":10,"successCount":10},"failure":{"count":10,"successCount":10,"errorRate":0},"recoveryMs":200,"conclusion":"System tolerated the fault.","assertions":[{"name":"error_rate","pass":true,"detail":"error rate 0%"}]}`)
	save("ci-smoke", false, `{"baseline":{"count":10,"successCount":10},"failure":{"count":10,"successCount":2,"errorRate":0.8},"recoveryMs":2400,"conclusion":"System degraded significantly.","assertions":[{"name":"error_rate","pass":false,"detail":"error rate 80%"}]}`)
	save("payment-latency", true, `{"baseline":{"count":10,"successCount":10},"failure":{"count":10,"successCount":0,"errorRate":1},"recoveryMs":100,"conclusion":"System degraded significantly.","assertions":[{"name":"error_rate","pass":false,"detail":"error rate 100%"}]}`)
	srv := &Server{Store: db, Runner: &experiment.Runner{Store: db}}
	req := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	rec := httptest.NewRecorder()
	srv.history(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		History []struct {
			Name     string `json:"name"`
			PassText string `json:"passText"`
			Judged   int    `json:"judged"`
			KnownBad []struct {
				ID int64 `json:"id"`
			} `json:"knownBad"`
			Recovery []struct {
				RecoveryMs *int64 `json:"recoveryMs"`
			} `json:"recovery"`
		} `json:"history"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.History) != 2 || body.History[0].PassText != "1/2" || len(body.History[0].Recovery) != 2 {
		t.Fatalf("history = %#v", body.History)
	}
	if body.History[1].Name != "payment-latency" || body.History[1].Judged != 0 || len(body.History[1].KnownBad) != 1 {
		t.Fatalf("known bad = %#v", body.History[1])
	}
}
