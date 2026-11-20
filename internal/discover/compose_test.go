package discover

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeComposeDependsLabel(t *testing.T) {
	got := normalizeDepends("payment-service:service_started:false,inventory-service:service_started:false")
	if len(got) != 2 || got[0] != "inventory-service" || got[1] != "payment-service" {
		t.Fatalf("depends = %#v", got)
	}
}

func TestParseComposeDependsOnStyles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	body := []byte(`
name: crashlab-demo
services:
  storefront:
    depends_on:
      - checkout-service
      - inventory-service
  checkout-service:
    depends_on:
      payment-service:
        condition: service_started
  payment-service:
    image: pay
  inventory-service:
    depends_on:
      postgres:
        condition: service_healthy
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	graph, err := ParseCompose(path)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Project != "crashlab-demo" {
		t.Fatalf("project = %s", graph.Project)
	}
	if got := graph.Dependencies["storefront"]; len(got) != 2 || got[0] != "checkout-service" || got[1] != "inventory-service" {
		t.Fatalf("storefront deps = %#v", got)
	}
	if got := graph.Dependencies["checkout-service"]; len(got) != 1 || got[0] != "payment-service" {
		t.Fatalf("checkout deps = %#v", got)
	}
	if got := graph.Dependencies["inventory-service"]; len(got) != 1 || got[0] != "postgres" {
		t.Fatalf("inventory deps = %#v", got)
	}
}
