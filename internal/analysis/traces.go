package analysis

import (
	"strings"

	"github.com/dkolancs/crashlab/internal/model"
)

func mergeTraceDependencies(services []model.Service, samples []model.Sample) []model.Service {
	extra := map[string]map[string]bool{}
	for _, sm := range samples {
		if sm.Source != model.SourceTrace || sm.Name != model.MetricTrace {
			continue
		}
		from, to, ok := strings.Cut(sm.Service, ">")
		if !ok || from == "" || to == "" {
			continue
		}
		if extra[from] == nil {
			extra[from] = map[string]bool{}
		}
		extra[from][to] = true
	}
	if len(extra) == 0 {
		return services
	}
	known := map[string]int{}
	for i, svc := range services {
		known[svc.Name] = i
	}
	for from, tos := range extra {
		deps := make([]string, 0, len(tos))
		for to := range tos {
			deps = append(deps, to)
		}
		idx, ok := known[from]
		if !ok {
			services = append(services, model.Service{Name: from, DependsOn: deps})
			continue
		}
		for _, dep := range deps {
			if !contains(services[idx].DependsOn, dep) {
				services[idx].DependsOn = append(services[idx].DependsOn, dep)
			}
		}
	}
	return services
}

func pathNote(samples []model.Sample) string {
	for _, sm := range samples {
		if sm.Source == model.SourceTrace {
			return "Request paths include observed trace edges."
		}
	}
	return "Timestamp correlation along declared dependencies."
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
