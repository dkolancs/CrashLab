package experiment

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dkolancs/crashlab/internal/model"
)

func TestParseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payment-latency.yaml")
	body := []byte(`
name: payment-latency
target:
  service: payment-service
fault:
  type: latency
  delay: 800ms
experiment:
  baseline: 15s
  duration: 30s
  recovery: 15s
expect:
  error_rate: "< 5%"
  p95_latency: "< 1000ms"
  recovery_time: "< 10s"
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "payment-latency" || spec.Target != "payment-service" || spec.FaultType != model.FaultLatency {
		t.Fatalf("spec = %#v", spec)
	}
	if spec.Delay != 800*time.Millisecond || spec.Duration != 30*time.Second || len(spec.Assertions) != 3 {
		t.Fatalf("spec timings = %#v", spec)
	}
}
