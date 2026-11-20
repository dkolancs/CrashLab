package discover

import (
	"testing"

	"github.com/dkolancs/crashlab/internal/model"
)

func TestSuggestUsesTransitiveDependents(t *testing.T) {
	services := []model.Service{
		{Name: "storefront", DependsOn: []string{"checkout-service"}},
		{Name: "checkout-service", DependsOn: []string{"payment-service"}},
		{Name: "payment-service"},
		{Name: "inventory-service"},
	}
	got := Suggest(services, "storefront")
	if len(got) != 1 || got[0].Service != "payment-service" {
		t.Fatalf("suggestions = %#v", got)
	}
	if len(got[0].Faults) != 3 {
		t.Fatalf("faults = %#v", got[0].Faults)
	}
}

func TestSuggestSkipsDatastoreWithoutEntryHealthcheck(t *testing.T) {
	services := []model.Service{
		{Name: "storefront", DependsOn: []string{"inventory-service"}},
		{Name: "inventory-service", DependsOn: []string{"postgres"}, DependsHealthy: []string{"postgres"}},
		{Name: "postgres", Image: "postgres:16-alpine"},
	}
	got := Suggest(services, "storefront")
	for _, item := range got {
		if item.Service == "postgres" {
			t.Fatalf("postgres suggested: %#v", got)
		}
	}
	healthy := append([]model.Service{}, services...)
	healthy[0].DependsHealthy = []string{"postgres"}
	got = Suggest(healthy, "storefront")
	found := false
	for _, item := range got {
		if item.Service == "postgres" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected postgres when the entry health-checks it: %#v", got)
	}
}
