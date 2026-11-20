package discover

import (
	"sort"

	"github.com/dkolancs/crashlab/internal/model"
)

// Suggestion is one fault worth running against a service that other services depend on.
type Suggestion struct {
	Service string   `json:"service"`
	Reasons []string `json:"reasons"`
	Faults  []string `json:"faults"`
}

// Suggest recommends latency, outage, and packet loss for services with at least two transitive dependents.
func Suggest(services []model.Service) []Suggestion {
	var out []Suggestion
	for _, svc := range services {
		count := transitiveDependents(services, svc.Name)
		if count < 2 {
			continue
		}
		out = append(out, Suggestion{
			Service: svc.Name,
			Reasons: []string{"depended on by " + itoa(count) + " services"},
			Faults:  []string{model.FaultLatency, model.FaultKill, model.FaultLoss},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

func transitiveDependents(services []model.Service, name string) int {
	rev := map[string][]string{}
	for _, svc := range services {
		for _, dep := range svc.DependsOn {
			rev[dep] = append(rev[dep], svc.Name)
		}
	}
	seen := map[string]bool{}
	queue := append([]string{}, rev[name]...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] || cur == name {
			continue
		}
		seen[cur] = true
		queue = append(queue, rev[cur]...)
	}
	return len(seen)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
