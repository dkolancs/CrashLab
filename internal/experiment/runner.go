package experiment

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/dkolancs/crashlab/internal/analysis"
	"github.com/dkolancs/crashlab/internal/discover"
	"github.com/dkolancs/crashlab/internal/dockerx"
	"github.com/dkolancs/crashlab/internal/fault"
	"github.com/dkolancs/crashlab/internal/metrics"
	"github.com/dkolancs/crashlab/internal/model"
	"github.com/dkolancs/crashlab/internal/store"
	"github.com/docker/docker/client"
)

// Spec is a request to run one experiment.
type Spec struct {
	Name         string
	Target       string
	FaultType    string
	Delay        time.Duration
	LossPercent  float64
	CPUPercent   float64
	Baseline     time.Duration
	Duration     time.Duration
	Recovery     time.Duration
	EntryURL     string
	EntryService string
	ComposeFile  string
	Project      string
	NetemDir     string
	Assertions   []analysis.Assertion
}

// ApplyDefaults fills the v0.1 demo settings.
func ApplyDefaults(s Spec) Spec {
	if s.Name == "" {
		s.Name = "payment-latency"
	}
	if s.Target == "" {
		s.Target = "payment-service"
	}
	if s.FaultType == "" {
		s.FaultType = model.FaultLatency
	}
	if s.FaultType == model.FaultLatency && s.Delay == 0 {
		s.Delay = 800 * time.Millisecond
	}
	if s.FaultType == model.FaultLoss && s.LossPercent == 0 {
		s.LossPercent = 20
	}
	if s.FaultType == model.FaultCPU && s.CPUPercent == 0 {
		s.CPUPercent = 90
	}
	if s.Baseline == 0 {
		s.Baseline = 15 * time.Second
	}
	if s.Duration == 0 {
		s.Duration = 30 * time.Second
	}
	if s.Recovery == 0 {
		s.Recovery = 15 * time.Second
	}
	if s.EntryURL == "" {
		s.EntryURL = "http://127.0.0.1:8081/checkout"
	}
	if s.EntryService == "" {
		s.EntryService = "storefront"
	}
	if s.ComposeFile == "" {
		s.ComposeFile = "demo/compose.yaml"
	}
	if s.NetemDir == "" {
		s.NetemDir = "netem"
	}
	return s
}

// Runner executes one experiment at a time.
type Runner struct {
	Store *store.Store

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
}

// Start launches an experiment in the background.
func (r *Runner) Start(parent context.Context, spec Spec) (model.Experiment, error) {
	spec = ApplyDefaults(spec)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return model.Experiment{}, fmt.Errorf("an experiment is already running")
	}
	exp, err := r.Store.CreateExperiment(model.Experiment{
		Name:         spec.Name,
		Status:       model.StatusRunning,
		Phase:        model.PhaseSetup,
		Target:       spec.Target,
		FaultType:    spec.FaultType,
		Delay:        spec.Delay,
		LossPercent:  spec.LossPercent,
		CPUPercent:   spec.CPUPercent,
		Baseline:     spec.Baseline,
		Duration:     spec.Duration,
		Recovery:     spec.Recovery,
		EntryURL:     spec.EntryURL,
		EntryService: spec.EntryService,
		ComposeFile:  spec.ComposeFile,
		Project:      spec.Project,
		StartedAt:    time.Now().UTC(),
	})
	if err != nil {
		return exp, err
	}
	ctx, cancel := context.WithCancel(parent)
	r.running = true
	r.cancel = cancel
	go func() {
		defer func() {
			r.mu.Lock()
			r.running = false
			r.cancel = nil
			r.mu.Unlock()
		}()
		r.execute(ctx, exp, spec)
	}()
	return exp, nil
}

// Run executes an experiment and waits for it to finish.
func (r *Runner) Run(ctx context.Context, spec Spec) (model.Experiment, error) {
	exp, err := r.Start(ctx, spec)
	if err != nil {
		return exp, err
	}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.Stop()
			return r.Store.GetExperiment(exp.ID)
		case <-ticker.C:
			current, err := r.Store.GetExperiment(exp.ID)
			if err != nil {
				return current, err
			}
			if current.Status == model.StatusCompleted || current.Status == model.StatusFailed {
				if current.Status == model.StatusFailed {
					return current, fmt.Errorf("%s", current.Error)
				}
				if _, summary, ok, err := r.Store.GetResult(current.ID); err == nil && ok && analysis.SummaryFailed(summary) {
					return current, fmt.Errorf("experiment failed assertions")
				}
				return current, nil
			}
		}
	}
}

// Stop cancels the active experiment. The runner clears any injected fault.
func (r *Runner) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Running reports whether an experiment is in progress in this process.
func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

func (r *Runner) execute(ctx context.Context, exp model.Experiment, spec Spec) {
	var runErr error
	var cli *client.Client
	var targetID string
	defer func() {
		if targetID != "" && cli != nil {
			clearCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			if err := fault.Clear(clearCtx, cli, targetID); err != nil {
				log.Printf("experiment %d: clear fault: %v", exp.ID, err)
			}
			cancel()
		}
		if cli != nil {
			cli.Close()
		}
		if runErr != nil && ctx.Err() != nil && runErr != context.Canceled {
			runErr = fmt.Errorf("%w", runErr)
		}
		if ctx.Err() != nil && runErr == nil {
			runErr = fmt.Errorf("experiment canceled")
		}
		if runErr != nil {
			r.fail(exp.ID, runErr)
		}
	}()

	cli, runErr = dockerx.New()
	if runErr != nil {
		return
	}
	log.Printf("experiment %d phase=setup target=%s delay=%s", exp.ID, spec.Target, spec.Delay)
	topo, err := discover.Inspect(ctx, cli, spec.ComposeFile, spec.Project)
	if err != nil {
		runErr = err
		return
	}
	exp.Project = topo.Project
	if err := r.Store.UpdateExperiment(exp); err != nil {
		runErr = err
		return
	}
	if err := r.Store.SaveServices(exp.ID, topo.Services); err != nil {
		runErr = err
		return
	}
	target, ok := topo.ByName(spec.Target)
	if !ok {
		runErr = fmt.Errorf("target service %q is not in project %q", spec.Target, topo.Project)
		return
	}
	if target.ContainerID == "" || target.Status != "running" {
		runErr = fmt.Errorf("target service %q is not running", spec.Target)
		return
	}
	targetID = target.ContainerID
	if err := fault.Prepare(ctx, spec.NetemDir); err != nil {
		runErr = err
		return
	}
	if err := fault.Clear(ctx, cli, targetID); err != nil {
		log.Printf("experiment %d: pre-clear: %v", exp.ID, err)
	}
	if err := metrics.WaitReady(ctx, spec.EntryURL, 60*time.Second); err != nil {
		runErr = err
		return
	}

	samples := make(chan model.Sample, 4096)
	phase := metrics.NewPhase(model.PhaseBaseline)
	loadCtx, stopLoad := context.WithCancel(ctx)
	scrapeCtx, stopScrape := context.WithCancel(ctx)
	var producers sync.WaitGroup
	producers.Add(2)
	go func() {
		defer producers.Done()
		metrics.GenerateLoad(loadCtx, spec.EntryURL, spec.EntryService, phase, samples)
	}()
	go func() {
		defer producers.Done()
		metrics.Scrape(scrapeCtx, cli, topo.Services, phase, samples)
	}()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		r.writeSamples(exp.ID, samples)
	}()
	stopCollectors := func() {
		stopLoad()
		stopScrape()
		producers.Wait()
		close(samples)
		<-writerDone
	}

	if err := r.setPhase(exp.ID, model.PhaseBaseline); err != nil {
		stopCollectors()
		runErr = err
		return
	}
	log.Printf("experiment %d phase=baseline duration=%s", exp.ID, spec.Baseline)
	if err := sleep(ctx, spec.Baseline); err != nil {
		stopCollectors()
		runErr = err
		return
	}

	phase.Set(model.PhaseInject)
	if err := r.setPhase(exp.ID, model.PhaseInject); err != nil {
		stopCollectors()
		runErr = err
		return
	}
	log.Printf("experiment %d phase=inject", exp.ID)
	injectedAt := time.Now().UTC()
	if err := fault.Apply(ctx, cli, targetID, fault.Request{
		Kind:    spec.FaultType,
		Delay:   spec.Delay,
		Percent: faultPercent(spec),
	}); err != nil {
		stopCollectors()
		runErr = err
		return
	}
	if err := r.Store.UpsertFault(model.Fault{
		ExperimentID: exp.ID, Service: spec.Target, Type: spec.FaultType, Delay: spec.Delay,
		LossPercent: spec.LossPercent, CPUPercent: spec.CPUPercent, InjectedAt: &injectedAt,
	}); err != nil {
		stopCollectors()
		runErr = err
		return
	}
	if err := r.Store.AddEvent(model.Event{
		ExperimentID: exp.ID, At: injectedAt, Kind: model.KindFaultInjected, Service: spec.Target, Message: faultMessage(spec),
	}); err != nil {
		stopCollectors()
		runErr = err
		return
	}

	phase.Set(model.PhaseObserve)
	if err := r.setPhase(exp.ID, model.PhaseObserve); err != nil {
		stopCollectors()
		runErr = err
		return
	}
	log.Printf("experiment %d phase=observe duration=%s", exp.ID, spec.Duration)
	if err := sleep(ctx, spec.Duration); err != nil {
		stopCollectors()
		runErr = err
		return
	}

	phase.Set(model.PhaseRemove)
	if err := r.setPhase(exp.ID, model.PhaseRemove); err != nil {
		stopCollectors()
		runErr = err
		return
	}
	log.Printf("experiment %d phase=remove", exp.ID)
	removedAt := time.Now().UTC()
	if err := fault.Clear(ctx, cli, targetID); err != nil {
		stopCollectors()
		runErr = err
		return
	}
	targetID = ""
	faultRow, _, _ := r.Store.GetFault(exp.ID)
	faultRow.ExperimentID = exp.ID
	faultRow.Service = spec.Target
	faultRow.Type = spec.FaultType
	faultRow.Delay = spec.Delay
	faultRow.LossPercent = spec.LossPercent
	faultRow.CPUPercent = spec.CPUPercent
	if faultRow.InjectedAt == nil {
		faultRow.InjectedAt = &injectedAt
	}
	faultRow.RemovedAt = &removedAt
	if err := r.Store.UpsertFault(faultRow); err != nil {
		stopCollectors()
		runErr = err
		return
	}
	if err := r.Store.AddEvent(model.Event{
		ExperimentID: exp.ID, At: removedAt, Kind: model.KindFaultRemoved, Service: spec.Target, Message: "Fault removed",
	}); err != nil {
		stopCollectors()
		runErr = err
		return
	}

	phase.Set(model.PhaseRecovery)
	if err := r.setPhase(exp.ID, model.PhaseRecovery); err != nil {
		stopCollectors()
		runErr = err
		return
	}
	log.Printf("experiment %d phase=recovery duration=%s", exp.ID, spec.Recovery)
	if err := sleep(ctx, spec.Recovery); err != nil {
		stopCollectors()
		runErr = err
		return
	}

	phase.Set(model.PhaseAnalysis)
	_ = r.setPhase(exp.ID, model.PhaseAnalysis)
	stopCollectors()
	log.Printf("experiment %d phase=analysis", exp.ID)
	if err := r.analyze(exp.ID, spec, injectedAt, removedAt); err != nil {
		runErr = err
		return
	}
	finished := time.Now().UTC()
	current, err := r.Store.GetExperiment(exp.ID)
	if err != nil {
		runErr = err
		return
	}
	current.Status = model.StatusCompleted
	current.Phase = model.PhaseDone
	current.FinishedAt = &finished
	current.Error = ""
	if err := r.Store.UpdateExperiment(current); err != nil {
		runErr = err
		return
	}
	log.Printf("experiment %d phase=done", exp.ID)
}

func faultPercent(spec Spec) float64 {
	if spec.FaultType == model.FaultCPU {
		return spec.CPUPercent
	}
	return spec.LossPercent
}

func faultMessage(spec Spec) string {
	switch spec.FaultType {
	case model.FaultLoss:
		return fmt.Sprintf("Packet loss injected (%.0f%%)", spec.LossPercent)
	case model.FaultKill:
		return "Container terminated"
	case model.FaultCPU:
		return fmt.Sprintf("CPU stress injected (%.0f%%)", spec.CPUPercent)
	default:
		return "Fault injected"
	}
}

func (r *Runner) analyze(id int64, spec Spec, injectedAt, removedAt time.Time) error {
	exp, err := r.Store.GetExperiment(id)
	if err != nil {
		return err
	}
	samples, err := r.Store.ListSamples(id)
	if err != nil {
		return err
	}
	services, err := r.Store.ListServices(id)
	if err != nil {
		return err
	}
	outcome := analysis.Evaluate(exp, samples, services, injectedAt, removedAt)
	outcome.Assertions = analysis.Judge(outcome, spec.Assertions)
	outcome.Explanation = analysis.Explain(exp, outcome)
	for _, ev := range outcome.Derived {
		if err := r.Store.AddEvent(ev); err != nil {
			return err
		}
	}
	events, err := r.Store.ListEvents(id)
	if err != nil {
		return err
	}
	text := analysis.Format(exp, outcome, events)
	payload := struct {
		analysis.Outcome
		Text string `json:"text"`
	}{Outcome: outcome, Text: text}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return r.Store.SaveResult(id, text, string(raw))
}

func (r *Runner) writeSamples(id int64, samples <-chan model.Sample) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var batch []model.Sample
	flush := func() {
		if len(batch) == 0 {
			return
		}
		for i := range batch {
			batch[i].ExperimentID = id
		}
		if err := r.Store.InsertSamples(batch); err != nil {
			log.Printf("experiment %d: write samples: %v", id, err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case sample, ok := <-samples:
			if !ok {
				flush()
				return
			}
			batch = append(batch, sample)
			if len(batch) >= 400 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (r *Runner) setPhase(id int64, phase string) error {
	exp, err := r.Store.GetExperiment(id)
	if err != nil {
		return err
	}
	exp.Phase = phase
	return r.Store.UpdateExperiment(exp)
}

func (r *Runner) fail(id int64, reason error) {
	log.Printf("experiment %d failed: %v", id, reason)
	exp, err := r.Store.GetExperiment(id)
	if err != nil {
		log.Printf("experiment %d: %v", id, err)
		return
	}
	if exp.Status == model.StatusCompleted {
		return
	}
	now := time.Now().UTC()
	exp.Status = model.StatusFailed
	exp.Phase = model.PhaseDone
	exp.Error = reason.Error()
	exp.FinishedAt = &now
	if err := r.Store.UpdateExperiment(exp); err != nil {
		log.Printf("experiment %d: %v", id, err)
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
