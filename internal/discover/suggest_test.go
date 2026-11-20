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
	got := Suggest(services)
	if len(got) != 1 || got[0].Service != "payment-service" {
		t.Fatalf("suggestions = %#v", got)
	}
	if len(got[0].Faults) != 3 {
		t.Fatalf("faults = %#v", got[0].Faults)
	}
}
