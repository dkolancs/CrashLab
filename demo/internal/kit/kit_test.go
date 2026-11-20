package kit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCallContinuesTraceparent(t *testing.T) {
	traceID := strings.Repeat("ab", 16)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	parent, err := http.NewRequest(http.MethodGet, "http://storefront/checkout", nil)
	if err != nil {
		t.Fatal(err)
	}
	parent.Header.Set("traceparent", "00-"+traceID+"-"+strings.Repeat("cd", 8)+"-01")
	if _, _, err := Call(context.Background(), parent, "storefront", "checkout-service", srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Call(context.Background(), parent, "checkout-service", "payment-service", srv.URL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, traceID) {
		t.Fatalf("downstream traceparent = %s", got)
	}
	spans := snapshotTraces()
	if len(spans) < 2 {
		t.Fatalf("spans = %#v", spans)
	}
	last := spans[len(spans)-2:]
	if last[0].TraceID != traceID || last[1].TraceID != traceID || last[0].From != "storefront" || last[1].From != "checkout-service" {
		t.Fatalf("spans = %#v", last)
	}
}
