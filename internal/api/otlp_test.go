package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dkolancs/crashlab/internal/experiment"
	"github.com/dkolancs/crashlab/internal/model"
	"github.com/dkolancs/crashlab/internal/store"
)

func TestDefaultListenAddrIsLoopback(t *testing.T) {
	host, _, err := net.SplitHostPort(DefaultListenAddr)
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("addr = %s", DefaultListenAddr)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("host %s is not loopback", host)
	}
}

func TestOTLPEdgesAttachToRunningFault(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "crashlab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exp, err := db.CreateExperiment(model.Experiment{
		Name: "payment-latency", Status: model.StatusRunning, Phase: model.PhaseObserve,
		Target: "payment-service", StartedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	injected := time.Unix(0, 1_000_000_000).UTC()
	if err := db.UpsertFault(model.Fault{ExperimentID: exp.ID, Service: "payment-service", InjectedAt: &injected}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Store: db, Runner: &experiment.Runner{Store: db}}
	body := `{
	  "resourceSpans": [
	    {"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "storefront"}}]},
	     "scopeSpans": [{"spans": [{"spanId": "parent", "kind": 2, "startTimeUnixNano": "1000000000", "endTimeUnixNano": "1800000000"}]}]},
	    {"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "checkout-service"}}]},
	     "scopeSpans": [{"spans": [{"spanId": "child", "parentSpanId": "parent", "kind": 2, "startTimeUnixNano": "1200000000", "endTimeUnixNano": "1600000000"}]}]}
	  ]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.otlpTraces(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	samples, err := db.ListSamples(exp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].Service != "storefront>checkout-service" || samples[0].Source != model.SourceTrace {
		t.Fatalf("samples = %#v", samples)
	}
}
