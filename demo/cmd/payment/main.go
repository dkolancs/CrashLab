package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/dkolancs/crashlab/demo/internal/kit"
)

func main() {
	stats := &kit.Stats{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /charge", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		stats.Record(false, 30*time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	kit.Serve(stats, mux)
}
