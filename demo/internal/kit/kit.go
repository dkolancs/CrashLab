package kit

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// Stats tracks request counters and the last downstream call.
type Stats struct {
	mu                  sync.Mutex
	Requests            int64
	Errors              int64
	DownstreamLatencyMS float64
}

// Record updates counters after one inbound request.
func (s *Stats) Record(failed bool, downstream time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests++
	if failed {
		s.Errors++
	}
	s.DownstreamLatencyMS = float64(downstream.Microseconds()) / 1000
}

func (s *Stats) snapshot() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{Requests: s.Requests, Errors: s.Errors, DownstreamLatencyMS: s.DownstreamLatencyMS}
}

// Serve registers health and stats routes, then listens on PORT (default 8080).
func Serve(stats *Stats, mux *http.ServeMux) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		snap := stats.snapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"requests":              snap.Requests,
			"errors":                snap.Errors,
			"downstream_latency_ms": snap.DownstreamLatencyMS,
		})
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

// client disables keep-alive so a redirected port affects the next call
// instead of an already open connection.
var client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

// Get performs an HTTP GET and returns the status, body, and error.
func Get(req *http.Request) (int, []byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}
