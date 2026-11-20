package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dkolancs/crashlab/internal/model"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// Phase is the current experiment phase, read by collectors when a sample is taken.
type Phase struct {
	value atomic.Value
}

// NewPhase returns a phase holder starting at the given value.
func NewPhase(initial string) *Phase {
	p := &Phase{}
	p.value.Store(initial)
	return p
}

// Set updates the phase stamped onto new samples.
func (p *Phase) Set(phase string) {
	p.value.Store(phase)
}

// Get returns the current phase.
func (p *Phase) Get() string {
	v, _ := p.value.Load().(string)
	return v
}

type statsBody struct {
	Requests            float64 `json:"requests"`
	Errors              float64 `json:"errors"`
	DownstreamLatencyMS float64 `json:"downstream_latency_ms"`
}

type traceEdge struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	LatencyMs float64   `json:"latencyMs"`
	At        time.Time `json:"at"`
}

// GenerateLoad issues a steady 10 requests per second until ctx is canceled.
// The rate stays open-loop so retries inside the app show up as extra downstream traffic.
func GenerateLoad(ctx context.Context, url, service string, phase *Phase, out chan<- model.Sample) {
	client := &http.Client{Timeout: 8 * time.Second}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var wg sync.WaitGroup
	var inflight atomic.Int32
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-ticker.C:
			if inflight.Load() >= 40 {
				continue
			}
			inflight.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer inflight.Add(-1)
				start := time.Now()
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				if err != nil {
					return
				}
				resp, err := client.Do(req)
				if ctx.Err() != nil {
					if resp != nil {
						resp.Body.Close()
					}
					return
				}
				latency := float64(time.Since(start).Microseconds()) / 1000
				failed := err != nil
				if resp != nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if resp.StatusCode >= 500 {
						failed = true
					}
				}
				now := time.Now()
				errValue := 0.0
				if failed {
					errValue = 1
				}
				ph := phase.Get()
				send(ctx, out, model.Sample{At: now, Phase: ph, Source: model.SourceLoad, Service: service, Name: model.MetricLatencyMS, Value: latency})
				send(ctx, out, model.Sample{At: now, Phase: ph, Source: model.SourceLoad, Service: service, Name: model.MetricError, Value: errValue})
			}()
		}
	}
}

// Scrape polls /stats or HTTP ports and Docker counters until ctx is canceled.
func Scrape(ctx context.Context, cli *client.Client, services []model.Service, phase *Phase, out chan<- model.Sample) {
	httpClient := &http.Client{}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	seenTraces := &traceSeen{ids: map[string]bool{}}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sampleOnce(ctx, cli, httpClient, services, phase.Get(), out, seenTraces)
		}
	}
}

type traceSeen struct {
	mu  sync.Mutex
	ids map[string]bool
}

func (s *traceSeen) mark(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids[id] {
		return false
	}
	s.ids[id] = true
	return true
}

func sampleOnce(ctx context.Context, cli *client.Client, httpClient *http.Client, services []model.Service, phase string, out chan<- model.Sample, seenTraces *traceSeen) {
	var wg sync.WaitGroup
	for _, svc := range services {
		svc := svc
		wg.Add(1)
		go func() {
			defer wg.Done()
			now := time.Now()
			if len(svc.HostPorts) > 0 {
				base := fmt.Sprintf("http://127.0.0.1:%d", svc.HostPorts[0])
				if body, ok := fetchStats(ctx, httpClient, base+"/stats"); ok {
					send(ctx, out, model.Sample{At: now, Phase: phase, Source: model.SourceStats, Service: svc.Name, Name: model.MetricLatencyMS, Value: body.DownstreamLatencyMS})
					send(ctx, out, model.Sample{At: now, Phase: phase, Source: model.SourceStats, Service: svc.Name, Name: model.MetricRequests, Value: body.Requests})
					send(ctx, out, model.Sample{At: now, Phase: phase, Source: model.SourceStats, Service: svc.Name, Name: model.MetricErrors, Value: body.Errors})
				}
				for _, edge := range fetchTraces(ctx, httpClient, base+"/traces") {
					if edge.ID == "" || !seenTraces.mark(edge.ID) {
						continue
					}
					at := edge.At
					if at.IsZero() {
						at = now
					}
					send(ctx, out, model.Sample{
						At: at, Phase: phase, Source: model.SourceTrace, Service: edge.From + ">" + edge.To,
						Name: model.MetricTrace, Value: edge.LatencyMs,
					})
				}
			}
			if svc.ContainerID == "" || cli == nil {
				return
			}
			cpu, rx := dockerCounters(ctx, cli, svc.ContainerID)
			send(ctx, out, model.Sample{At: now, Phase: phase, Source: model.SourceDocker, Service: svc.Name, Name: model.MetricCPU, Value: cpu})
			send(ctx, out, model.Sample{At: now, Phase: phase, Source: model.SourceDocker, Service: svc.Name, Name: model.MetricRxPackets, Value: rx})
		}()
	}
	wg.Wait()
}

func fetchTraces(ctx context.Context, client *http.Client, url string) []traceEdge {
	ctx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var edges []traceEdge
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&edges); err != nil {
		return nil
	}
	return edges
}

func fetchStats(ctx context.Context, client *http.Client, url string) (statsBody, bool) {
	ctx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return statsBody{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return statsBody{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statsBody{}, false
	}
	var body statsBody
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body); err != nil {
		return statsBody{}, false
	}
	return body, true
}

func dockerCounters(ctx context.Context, cli *client.Client, id string) (float64, float64) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := cli.ContainerStats(ctx, id, false)
	if err != nil {
		return 0, 0
	}
	defer resp.Body.Close()
	var stats container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return 0, 0
	}
	return cpuPercent(stats), rxPackets(stats)
}

func cpuPercent(stats container.StatsResponse) float64 {
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)
	if cpuDelta <= 0 || sysDelta <= 0 {
		return 0
	}
	online := stats.CPUStats.OnlineCPUs
	if online == 0 {
		online = uint32(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if online == 0 {
		online = 1
	}
	return (cpuDelta / sysDelta) * float64(online) * 100
}

func rxPackets(stats container.StatsResponse) float64 {
	var total uint64
	for _, net := range stats.Networks {
		total += net.RxPackets
	}
	return float64(total)
}

func send(ctx context.Context, out chan<- model.Sample, sample model.Sample) {
	select {
	case <-ctx.Done():
	case out <- sample:
	}
}

// WaitReady polls a URL until it returns a non-5xx response.
func WaitReady(ctx context.Context, url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
			last = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			if last == nil {
				last = ctx.Err()
			}
			return fmt.Errorf("entry url %s not ready: %w", url, last)
		case <-time.After(500 * time.Millisecond):
		}
	}
}
