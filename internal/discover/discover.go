package discover

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dkolancs/crashlab/internal/model"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// Inspect lists a Compose project's containers and merges declared dependencies.
func Inspect(ctx context.Context, cli *client.Client, composePath, project string) (model.Topology, error) {
	abs, err := filepath.Abs(composePath)
	if err != nil {
		return model.Topology{}, err
	}
	graph, err := ParseCompose(abs)
	if err != nil {
		return model.Topology{}, err
	}
	if project == "" {
		project = graph.Project
	}
	if project == "" {
		project = filepath.Base(filepath.Dir(abs))
	}

	containers, err := listProjectContainers(ctx, cli, project, abs)
	if err != nil {
		return model.Topology{}, err
	}
	if len(containers) == 0 {
		return model.Topology{}, fmt.Errorf("no containers for project %q; start them with docker compose -f %s up --build -d", project, composePath)
	}

	services := make([]model.Service, 0, len(containers))
	for _, ctr := range containers {
		inspected, err := cli.ContainerInspect(ctx, ctr.ID)
		if err != nil {
			return model.Topology{}, fmt.Errorf("inspect %s: %w", ctr.ID, err)
		}
		labels := inspected.Config.Labels
		name := labels["com.docker.compose.service"]
		if name == "" {
			continue
		}
		if labels["com.docker.compose.oneoff"] == "True" {
			continue
		}
		svc := model.Service{
			Name:         name,
			ContainerID:  inspected.ID,
			Image:        inspected.Config.Image,
			DependsOn:    dependencies(labels, graph.Dependencies[name]),
			Status:       inspected.State.Status,
			Health:       healthOf(inspected),
			RestartCount: inspected.RestartCount,
			Ports:        []string{},
			HostPorts:    []int{},
			Networks:     []string{},
		}
		svc.Ports, svc.HostPorts = publishedPorts(inspected)
		for network := range inspected.NetworkSettings.Networks {
			svc.Networks = append(svc.Networks, network)
		}
		sort.Strings(svc.Networks)
		services = append(services, svc)
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	if len(services) == 0 {
		return model.Topology{}, fmt.Errorf("project %q has containers but no compose service labels", project)
	}
	return model.Topology{Project: project, Services: services}, nil
}

func listProjectContainers(ctx context.Context, cli *client.Client, project, absCompose string) ([]types.Container, error) {
	listed, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+project)),
	})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	running := filterUp(listed)
	if len(running) > 0 {
		return running, nil
	}
	listed, err = cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.service")),
	})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	var matched []types.Container
	for _, ctr := range listed {
		files := ctr.Labels["com.docker.compose.project.config_files"]
		if files != "" && strings.Contains(files, absCompose) {
			matched = append(matched, ctr)
		}
	}
	return filterUp(matched), nil
}

func filterUp(in []types.Container) []types.Container {
	var out []types.Container
	for _, ctr := range in {
		if ctr.Labels["com.docker.compose.oneoff"] == "True" {
			continue
		}
		if ctr.State == "running" {
			out = append(out, ctr)
		}
	}
	return out
}

func dependencies(labels map[string]string, fromFile []string) []string {
	raw := labels["com.docker.compose.depends_on"]
	if raw != "" {
		out := normalizeDepends(raw)
		if len(out) > 0 {
			return out
		}
	}
	if fromFile == nil {
		return []string{}
	}
	return fromFile
}

// normalizeDepends reads Compose's depends_on label.
// Current Compose writes "payment-service:service_started:false" entries separated by commas.
func normalizeDepends(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' })
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := part
		if i := strings.IndexByte(part, ':'); i > 0 {
			name = part[:i]
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func healthOf(inspected types.ContainerJSON) string {
	if inspected.State == nil {
		return "unknown"
	}
	if inspected.State.Health != nil && inspected.State.Health.Status != "" {
		return inspected.State.Health.Status
	}
	if inspected.State.Status != "" {
		return inspected.State.Status
	}
	return "unknown"
}

func publishedPorts(inspected types.ContainerJSON) ([]string, []int) {
	type item struct {
		label     string
		host      int
		preferred bool
	}
	var items []item
	if inspected.NetworkSettings == nil {
		return []string{}, []int{}
	}
	for port, bindings := range inspected.NetworkSettings.Ports {
		preferred := port.Port() == "8080"
		for _, binding := range bindings {
			if binding.HostPort == "" {
				continue
			}
			host, err := strconv.Atoi(binding.HostPort)
			if err != nil {
				continue
			}
			items = append(items, item{
				label:     fmt.Sprintf("%s->%s", binding.HostPort, port),
				host:      host,
				preferred: preferred,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].preferred != items[j].preferred {
			return items[i].preferred
		}
		return items[i].host < items[j].host
	})
	labels := make([]string, 0, len(items))
	hosts := make([]int, 0, len(items))
	for _, item := range items {
		labels = append(labels, item.label)
		hosts = append(hosts, item.host)
	}
	if labels == nil {
		labels = []string{}
		hosts = []int{}
	}
	return labels, hosts
}
