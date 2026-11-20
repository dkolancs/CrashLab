package experiment

import (
	"fmt"
	"os"
	"time"

	"github.com/dkolancs/crashlab/internal/analysis"
	"github.com/dkolancs/crashlab/internal/model"
	"gopkg.in/yaml.v3"
)

type fileSpec struct {
	Name   string `yaml:"name"`
	Target struct {
		Service string `yaml:"service"`
	} `yaml:"target"`
	Fault struct {
		Type    string  `yaml:"type"`
		Delay   string  `yaml:"delay"`
		Percent float64 `yaml:"percent"`
	} `yaml:"fault"`
	Experiment struct {
		Baseline string `yaml:"baseline"`
		Duration string `yaml:"duration"`
		Recovery string `yaml:"recovery"`
	} `yaml:"experiment"`
	Entry struct {
		URL     string `yaml:"url"`
		Service string `yaml:"service"`
	} `yaml:"entry"`
	Compose string            `yaml:"compose"`
	Project string            `yaml:"project"`
	Expect  map[string]string `yaml:"expect"`
}

// ParseFile loads an experiment YAML file.
func ParseFile(path string) (Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, err
	}
	var file fileSpec
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return Spec{}, fmt.Errorf("parse %s: %w", path, err)
	}
	spec := Spec{
		Name:         file.Name,
		Target:       file.Target.Service,
		FaultType:    file.Fault.Type,
		LossPercent:  file.Fault.Percent,
		CPUPercent:   file.Fault.Percent,
		EntryURL:     file.Entry.URL,
		EntryService: file.Entry.Service,
		ComposeFile:  file.Compose,
		Project:      file.Project,
	}
	if file.Fault.Type == model.FaultCPU {
		spec.LossPercent = 0
	}
	if file.Fault.Type == model.FaultLoss {
		spec.CPUPercent = 0
	}
	if file.Fault.Delay != "" {
		d, err := time.ParseDuration(file.Fault.Delay)
		if err != nil {
			return Spec{}, fmt.Errorf("fault delay: %w", err)
		}
		spec.Delay = d
	}
	if spec.Baseline, err = parseOptional(file.Experiment.Baseline); err != nil {
		return Spec{}, fmt.Errorf("baseline: %w", err)
	}
	if spec.Duration, err = parseOptional(file.Experiment.Duration); err != nil {
		return Spec{}, fmt.Errorf("duration: %w", err)
	}
	if spec.Recovery, err = parseOptional(file.Experiment.Recovery); err != nil {
		return Spec{}, fmt.Errorf("recovery: %w", err)
	}
	for _, name := range []string{"error_rate", "p95_latency", "recovery_time"} {
		expr, ok := file.Expect[name]
		if !ok {
			continue
		}
		rule, err := analysis.ParseAssertion(name, expr)
		if err != nil {
			return Spec{}, err
		}
		spec.Assertions = append(spec.Assertions, rule)
	}
	return spec, nil
}

func parseOptional(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	return time.ParseDuration(raw)
}
