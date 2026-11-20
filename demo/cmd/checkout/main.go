package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/dkolancs/crashlab/demo/internal/kit"
)

func main() {
	paymentURL := os.Getenv("PAYMENT_URL")
	if paymentURL == "" {
		paymentURL = "http://127.0.0.1:8083"
	}
	stats := &kit.Stats{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /checkout", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		const attempts = 4
		var lastStatus int
		ok := false
		for i := 0; i < attempts; i++ {
			ctx, cancel := context.WithTimeout(r.Context(), 400*time.Millisecond)
			status, _, err := kit.Call(ctx, r, "checkout-service", "payment-service", paymentURL+"/charge")
			cancel()
			lastStatus = status
			if err == nil && status < 500 {
				ok = true
				break
			}
		}
		stats.Record(!ok, time.Since(start))
		if !ok {
			http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "upstream": lastStatus})
	})
	kit.Serve(stats, mux)
}
