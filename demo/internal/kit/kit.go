package kit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
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
	mux.HandleFunc("GET /traces", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshotTraces())
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

// Span is one observed downstream call.
type Span struct {
	ID        string    `json:"id"`
	TraceID   string    `json:"traceId"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	LatencyMs float64   `json:"latencyMs"`
	Error     bool      `json:"error"`
	At        time.Time `json:"at"`
}

var traceLog struct {
	mu    sync.Mutex
	items []Span
}

func recordTrace(span Span) {
	traceLog.mu.Lock()
	defer traceLog.mu.Unlock()
	traceLog.items = append(traceLog.items, span)
	if len(traceLog.items) > 200 {
		traceLog.items = traceLog.items[len(traceLog.items)-200:]
	}
}

func snapshotTraces() []Span {
	traceLog.mu.Lock()
	defer traceLog.mu.Unlock()
	out := make([]Span, len(traceLog.items))
	copy(out, traceLog.items)
	if out == nil {
		out = []Span{}
	}
	return out
}

// EnsureTrace stamps a traceparent onto an inbound request that does not already have one.
func EnsureTrace(r *http.Request) {
	if r == nil {
		return
	}
	if _, ok := traceParent(r); ok {
		return
	}
	r.Header.Set("traceparent", "00-"+randomHex(16)+"-"+randomHex(8)+"-01")
}

// Call performs a downstream GET and continues the inbound traceparent.
func Call(ctx context.Context, parent *http.Request, from, to, url string) (int, []byte, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	traceID, _ := traceParent(parent)
	if traceID == "" {
		traceID = randomHex(16)
	}
	spanID := randomHex(8)
	req.Header.Set("traceparent", "00-"+traceID+"-"+spanID+"-01")
	status, body, err := Get(req)
	recordTrace(Span{
		ID:        spanID,
		TraceID:   traceID,
		From:      from,
		To:        to,
		LatencyMs: float64(time.Since(start).Microseconds()) / 1000,
		Error:     err != nil || status >= 500,
		At:        time.Now().UTC(),
	})
	return status, body, err
}

func traceParent(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	parts := strings.Split(r.Header.Get("traceparent"), "-")
	if len(parts) < 4 || len(parts[1]) != 32 {
		return "", false
	}
	return parts[1], true
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

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
