package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/dkolancs/crashlab/internal/analysis"
	"github.com/dkolancs/crashlab/internal/discover"
	"github.com/dkolancs/crashlab/internal/dockerx"
	"github.com/dkolancs/crashlab/internal/experiment"
	"github.com/dkolancs/crashlab/internal/model"
	"github.com/dkolancs/crashlab/internal/store"
)

// Server is the local control API used by the dashboard.
type Server struct {
	Store    *store.Store
	Runner   *experiment.Runner
	Compose  string
	Project  string
	NetemDir string
	// Base is the process lifetime context. Experiments must not follow an HTTP request context.
	Base context.Context
}

type experimentDTO struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Status       string     `json:"status"`
	Phase        string     `json:"phase"`
	Target       string     `json:"target"`
	DelayMs      int64      `json:"delayMs"`
	BaselineMs   int64      `json:"baselineMs"`
	DurationMs   int64      `json:"durationMs"`
	RecoveryMs   int64      `json:"recoveryMs"`
	EntryURL     string     `json:"entryUrl"`
	EntryService string     `json:"entryService"`
	FaultType    string     `json:"faultType"`
	LossPercent  float64    `json:"lossPercent"`
	CPUPercent   float64    `json:"cpuPercent"`
	ComposeFile  string     `json:"composeFile"`
	Project      string     `json:"project"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	Error        string     `json:"error,omitempty"`
}

type snapshot struct {
	Experiment *experimentDTO    `json:"experiment"`
	Topology   model.Topology    `json:"topology"`
	Events     []model.Event     `json:"events"`
	NodeStates map[string]string `json:"nodeStates"`
	Buckets    []analysis.Bucket `json:"buckets"`
	Result     json.RawMessage   `json:"result"`
	Recent     []experimentDTO   `json:"recent"`
	Warning    string            `json:"warning,omitempty"`
}

type createRequest struct {
	Name         string  `json:"name"`
	Target       string  `json:"target"`
	DelayMs      int64   `json:"delayMs"`
	BaselineMs   int64   `json:"baselineMs"`
	DurationMs   int64   `json:"durationMs"`
	RecoveryMs   int64   `json:"recoveryMs"`
	EntryURL     string  `json:"entryUrl"`
	EntryService string  `json:"entryService"`
	FaultType    string  `json:"faultType"`
	LossPercent  float64 `json:"lossPercent"`
	CPUPercent   float64 `json:"cpuPercent"`
	ComposeFile  string  `json:"composeFile"`
	Project      string  `json:"project"`
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/topology", s.topology)
	mux.HandleFunc("GET /api/experiments", s.list)
	mux.HandleFunc("GET /api/compare", s.compare)
	mux.HandleFunc("GET /api/suggestions", s.suggestions)
	mux.HandleFunc("POST /api/experiments", s.create)
	mux.HandleFunc("POST /api/experiments/{id}/stop", s.stop)
	mux.HandleFunc("GET /api/experiments/{id}/stream", s.stream)
	mux.HandleFunc("GET /api/experiments/{id}/report", s.report)
	mux.HandleFunc("GET /api/experiments/{id}", s.get)
	return cors(mux)
}

// ListenAndServe serves until ctx is canceled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler()}
	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Server) compare(w http.ResponseWriter, r *http.Request) {
	left, err := strconv.ParseInt(r.URL.Query().Get("left"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "left id required"})
		return
	}
	right, err := strconv.ParseInt(r.URL.Query().Get("right"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "right id required"})
		return
	}
	leftSnap, err := s.snapshot(r.Context(), left)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	rightSnap, err := s.snapshot(r.Context(), right)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"left": leftSnap, "right": rightSnap})
}

func (s *Server) suggestions(w http.ResponseWriter, r *http.Request) {
	topo, err := s.liveTopology(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": discover.Suggest(topo.Services)})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) topology(w http.ResponseWriter, r *http.Request) {
	topo, err := s.liveTopology(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, topo)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshotLatest(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	snap, err := s.snapshot(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	text, _, ok, err := s.Store.GetResult(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "report not ready", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, text)
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	exp, err := s.Store.GetExperiment(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if exp.Status != model.StatusRunning {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "experiment is not running", "status": exp.Status})
		return
	}
	s.Runner.Stop()
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "stopping"})
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	spec := experiment.ApplyDefaults(experiment.Spec{
		Name:         req.Name,
		Target:       req.Target,
		FaultType:    req.FaultType,
		Delay:        ms(req.DelayMs),
		LossPercent:  req.LossPercent,
		CPUPercent:   req.CPUPercent,
		Baseline:     ms(req.BaselineMs),
		Duration:     ms(req.DurationMs),
		Recovery:     ms(req.RecoveryMs),
		EntryURL:     req.EntryURL,
		EntryService: req.EntryService,
		ComposeFile:  firstNonEmpty(req.ComposeFile, s.Compose),
		Project:      firstNonEmpty(req.Project, s.Project),
		NetemDir:     s.NetemDir,
	})
	parent := s.Base
	if parent == nil {
		parent = context.Background()
	}
	exp, err := s.Runner.Start(parent, spec)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": exp.ID, "status": exp.Status})
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	for {
		snap, err := s.snapshot(r.Context(), id)
		if err != nil {
			fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
			flusher.Flush()
			return
		}
		payload, err := json.Marshal(snap)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		if snap.Experiment != nil && (snap.Experiment.Status == model.StatusCompleted || snap.Experiment.Status == model.StatusFailed) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (s *Server) snapshotLatest(ctx context.Context) (snapshot, error) {
	exp, ok, err := s.Store.LatestExperiment()
	if err != nil {
		return snapshot{}, err
	}
	if !ok {
		topo, topoErr := s.liveTopology(ctx)
		recent, _ := s.recent()
		warning := ""
		if topoErr != nil {
			warning = topoErr.Error()
		}
		if topo.Services == nil {
			topo.Services = []model.Service{}
		}
		return snapshot{Topology: topo, Events: []model.Event{}, NodeStates: map[string]string{}, Buckets: []analysis.Bucket{}, Recent: recent, Warning: warning}, nil
	}
	return s.snapshot(ctx, exp.ID)
}

func (s *Server) snapshot(ctx context.Context, id int64) (snapshot, error) {
	exp, err := s.Store.GetExperiment(id)
	if err != nil {
		return snapshot{}, err
	}
	services, err := s.Store.ListServices(id)
	if err != nil {
		return snapshot{}, err
	}
	if len(services) == 0 {
		if topo, topoErr := s.liveTopology(ctx); topoErr == nil {
			services = topo.Services
		}
	}
	events, err := s.Store.ListEvents(id)
	if err != nil {
		return snapshot{}, err
	}
	if events == nil {
		events = []model.Event{}
	}
	samples, err := s.Store.ListSamples(id)
	if err != nil {
		return snapshot{}, err
	}
	fault, hasFault, err := s.Store.GetFault(id)
	if err != nil {
		return snapshot{}, err
	}
	injected := exp.StartedAt
	if hasFault && fault.InjectedAt != nil {
		injected = *fault.InjectedAt
	}
	nodeStates := map[string]string{}
	var buckets []analysis.Bucket
	if hasFault && fault.InjectedAt != nil {
		hops := analysis.Propagation(samples, services, exp.Target, injected)
		for _, hop := range hops {
			if hop.Service == exp.Target {
				nodeStates[hop.Service] = "fault"
			} else {
				nodeStates[hop.Service] = "degraded"
			}
		}
	}
	outcome := analysis.Evaluate(exp, samples, services, injected, time.Time{})
	buckets = outcome.Buckets
	if buckets == nil {
		buckets = []analysis.Bucket{}
	}
	dto := toDTO(exp)
	snap := snapshot{
		Experiment: &dto,
		Topology:   model.Topology{Project: exp.Project, Services: emptyServices(services)},
		Events:     events,
		NodeStates: nodeStates,
		Buckets:    buckets,
		Result:     json.RawMessage("null"),
	}
	if text, summary, ok, err := s.Store.GetResult(id); err == nil && ok {
		_ = text
		snap.Result = json.RawMessage(summary)
		var parsed struct {
			NodeStates map[string]string `json:"nodeStates"`
			Buckets    []analysis.Bucket `json:"buckets"`
		}
		if json.Unmarshal([]byte(summary), &parsed) == nil {
			if parsed.NodeStates != nil {
				snap.NodeStates = parsed.NodeStates
			}
			if len(parsed.Buckets) > 0 {
				snap.Buckets = parsed.Buckets
			}
		}
	}
	recent, err := s.recent()
	if err != nil {
		return snapshot{}, err
	}
	snap.Recent = recent
	return snap, nil
}

func (s *Server) liveTopology(ctx context.Context) (model.Topology, error) {
	cli, err := dockerx.New()
	if err != nil {
		return model.Topology{Project: s.Project, Services: []model.Service{}}, err
	}
	defer cli.Close()
	compose := s.Compose
	if compose == "" {
		compose = "demo/compose.yaml"
	}
	return discover.Inspect(ctx, cli, compose, s.Project)
}

func (s *Server) recent() ([]experimentDTO, error) {
	list, err := s.Store.ListExperiments(8)
	if err != nil {
		return nil, err
	}
	out := make([]experimentDTO, 0, len(list))
	for _, exp := range list {
		out = append(out, toDTO(exp))
	}
	return out, nil
}

func toDTO(exp model.Experiment) experimentDTO {
	return experimentDTO{
		ID:           exp.ID,
		Name:         exp.Name,
		Status:       exp.Status,
		Phase:        exp.Phase,
		Target:       exp.Target,
		DelayMs:      exp.Delay.Milliseconds(),
		BaselineMs:   exp.Baseline.Milliseconds(),
		DurationMs:   exp.Duration.Milliseconds(),
		RecoveryMs:   exp.Recovery.Milliseconds(),
		EntryURL:     exp.EntryURL,
		EntryService: exp.EntryService,
		FaultType:    exp.FaultType,
		LossPercent:  exp.LossPercent,
		CPUPercent:   exp.CPUPercent,
		ComposeFile:  exp.ComposeFile,
		Project:      exp.Project,
		StartedAt:    exp.StartedAt,
		FinishedAt:   exp.FinishedAt,
		Error:        exp.Error,
	}
}

func emptyServices(in []model.Service) []model.Service {
	if in == nil {
		return []model.Service{}
	}
	return in
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid experiment id")
	}
	return id, nil
}

func ms(v int64) time.Duration {
	if v <= 0 {
		return 0
	}
	return time.Duration(v) * time.Millisecond
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("write json: %v", err)
	}
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
