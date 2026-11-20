package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dkolancs/crashlab/internal/model"
)

func TestGenerateLoadRecordsOfferedAndCompleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1300*time.Millisecond)
	defer cancel()
	out := make(chan model.Sample, 512)
	done := make(chan struct{})
	var samples []model.Sample
	go func() {
		defer close(done)
		for sample := range out {
			samples = append(samples, sample)
		}
	}()
	GenerateLoad(ctx, srv.URL, "storefront", NewPhase(model.PhaseBaseline), out)
	close(out)
	<-done
	var offered, completed, requests int
	for _, sample := range samples {
		switch sample.Name {
		case model.MetricOffered:
			offered++
		case model.MetricCompleted:
			completed++
		case model.MetricLatencyMS:
			requests++
		}
	}
	if offered == 0 || completed == 0 || requests < 8 {
		t.Fatalf("offered=%d completed=%d requests=%d", offered, completed, requests)
	}
}
