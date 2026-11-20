package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/dkolancs/crashlab/demo/internal/kit"
)

func main() {
	checkoutURL := os.Getenv("CHECKOUT_URL")
	if checkoutURL == "" {
		checkoutURL = "http://127.0.0.1:8082"
	}
	inventoryURL := os.Getenv("INVENTORY_URL")
	if inventoryURL == "" {
		inventoryURL = "http://127.0.0.1:8084"
	}
	stats := &kit.Stats{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /checkout", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		var (
			wg          sync.WaitGroup
			stockStatus int
			stockBody   []byte
			stockErr    error
			checkStatus int
			checkBody   []byte
			checkErr    error
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, inventoryURL+"/stock", nil)
			if err != nil {
				stockErr = err
				return
			}
			stockStatus, stockBody, stockErr = kit.Get(req)
		}()
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkoutURL+"/checkout", nil)
			if err != nil {
				checkErr = err
				return
			}
			checkStatus, checkBody, checkErr = kit.Get(req)
		}()
		wg.Wait()

		failed := stockErr != nil || checkErr != nil || stockStatus >= 500 || checkStatus >= 500
		stats.Record(failed, time.Since(start))
		if failed {
			http.Error(w, "checkout failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stock":    json.RawMessage(stockBody),
			"checkout": json.RawMessage(checkBody),
		})
	})
	kit.Serve(stats, mux)
}
