package otel

import (
	"strings"
	"testing"
	"time"
)

func TestEdgesUseServerParentChildInsideWindow(t *testing.T) {
	windowStart := time.Unix(0, 1_000_000_000).UTC()
	windowEnd := time.Unix(0, 2_000_000_000).UTC()
	body := []byte(`{
	  "resourceSpans": [
	    {"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "storefront"}}]},
	     "scopeSpans": [{"spans": [{"spanId": "parent", "kind": 2, "startTimeUnixNano": "1000000000", "endTimeUnixNano": "1500000000"}]}]},
	    {"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "checkout-service"}}]},
	     "scopeSpans": [{"spans": [
	       {"spanId": "child", "parentSpanId": "parent", "kind": "SPAN_KIND_SERVER", "startTimeUnixNano": "1100000000", "endTimeUnixNano": "1400000000"},
	       {"spanId": "client", "parentSpanId": "parent", "kind": 3, "startTimeUnixNano": "1100000000", "endTimeUnixNano": "1400000000"},
	       {"spanId": "early", "parentSpanId": "parent", "kind": 2, "startTimeUnixNano": "100000000", "endTimeUnixNano": "200000000"}
	     ]}]}
	  ]
	}`)
	edges, err := Edges(body, windowStart, windowEnd)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 || edges[0].From != "storefront" || edges[0].To != "checkout-service" {
		t.Fatalf("edges = %#v", edges)
	}
	if edges[0].ID != "child" {
		t.Fatalf("id = %s", edges[0].ID)
	}
}

func TestEdgesRefuseMissingWindow(t *testing.T) {
	edges, err := Edges([]byte(`{"resourceSpans":[]}`), time.Time{}, time.Time{})
	if err != nil || len(edges) != 0 {
		t.Fatalf("edges=%#v err=%v", edges, err)
	}
	_, err = Edges([]byte(`{`), time.Unix(0, 1), time.Time{})
	if err == nil || !strings.Contains(err.Error(), "otlp") {
		t.Fatal(err)
	}
}
