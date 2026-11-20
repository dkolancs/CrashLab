package model

import "time"

const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"

	PhaseSetup    = "setup"
	PhaseBaseline = "baseline"
	PhaseInject   = "inject"
	PhaseObserve  = "observe"
	PhaseRemove   = "remove"
	PhaseRecovery = "recovery"
	PhaseAnalysis = "analysis"
	PhaseDone     = "done"

	SourceLoad   = "load"
	SourceStats  = "stats"
	SourceDocker = "docker"

	MetricLatencyMS = "latency_ms"
	MetricError     = "error"
	MetricRxPackets = "rx_packets"
	MetricCPU       = "cpu_pct"
	MetricRequests  = "requests_total"
	MetricErrors    = "errors_total"

	KindFaultInjected = "fault_injected"
	KindFaultRemoved  = "fault_removed"
	KindDegraded      = "degraded"
	KindTimeouts      = "timeouts"
	KindRetry         = "retry"
	KindRecovered     = "recovered"

	FaultLatency = "latency"
	FaultLoss    = "loss"
	FaultKill    = "kill"
	FaultCPU     = "cpu"

	SourceTrace = "trace"
	MetricTrace = "trace_ms"
)

// Service is one Compose service discovered from the Docker daemon.
type Service struct {
	Name         string   `json:"name"`
	ContainerID  string   `json:"containerId"`
	Image        string   `json:"image"`
	Ports        []string `json:"ports"`
	HostPorts    []int    `json:"hostPorts"`
	Networks     []string `json:"networks"`
	DependsOn    []string `json:"dependsOn"`
	Health       string   `json:"health"`
	Status       string   `json:"status"`
	RestartCount int      `json:"restartCount"`
}

// Topology is the project graph CrashLab draws and faults.
type Topology struct {
	Project  string    `json:"project"`
	Services []Service `json:"services"`
}

// ByName returns the service with the given compose name.
func (t Topology) ByName(name string) (Service, bool) {
	for _, s := range t.Services {
		if s.Name == name {
			return s, true
		}
	}
	return Service{}, false
}

// Experiment is one latency run and its phase machine.
type Experiment struct {
	ID           int64
	Name         string
	Status       string
	Phase        string
	Target       string
	Delay        time.Duration
	Baseline     time.Duration
	Duration     time.Duration
	Recovery     time.Duration
	EntryURL     string
	EntryService string
	ComposeFile  string
	Project      string
	FaultType    string
	LossPercent  float64
	CPUPercent   float64
	StartedAt    time.Time
	FinishedAt   *time.Time
	Error        string
}

// Sample is one timestamped measurement.
type Sample struct {
	ExperimentID int64
	At           time.Time
	Phase        string
	Source       string
	Service      string
	Name         string
	Value        float64
}

// Event is one line on the experiment timeline.
type Event struct {
	ID           int64     `json:"id"`
	ExperimentID int64     `json:"experimentId"`
	At           time.Time `json:"at"`
	Kind         string    `json:"kind"`
	Service      string    `json:"service"`
	Message      string    `json:"message"`
}

// Fault records when a fault was applied to a container.
type Fault struct {
	ExperimentID int64
	Service      string
	Type         string
	Delay        time.Duration
	LossPercent  float64
	CPUPercent   float64
	InjectedAt   *time.Time
	RemovedAt    *time.Time
}
