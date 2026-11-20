package discover

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	DependsOn yaml.Node `yaml:"depends_on"`
}

// ComposeGraph is the project name and declared dependencies from a compose file.
type ComposeGraph struct {
	Project      string
	Dependencies map[string][]string
}

// ParseCompose reads a compose file for the project name and depends_on edges.
func ParseCompose(path string) (ComposeGraph, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ComposeGraph{}, fmt.Errorf("read compose file: %w", err)
	}
	var file composeFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return ComposeGraph{}, fmt.Errorf("parse compose file: %w", err)
	}
	deps := map[string][]string{}
	for name, svc := range file.Services {
		list, err := parseDepends(svc.DependsOn)
		if err != nil {
			return ComposeGraph{}, fmt.Errorf("service %s depends_on: %w", name, err)
		}
		sort.Strings(list)
		deps[name] = list
	}
	return ComposeGraph{Project: file.Name, Dependencies: deps}, nil
}

func parseDepends(node yaml.Node) ([]string, error) {
	if node.Kind == 0 || len(node.Content) == 0 && node.Value == "" {
		return []string{}, nil
	}
	switch node.Kind {
	case yaml.SequenceNode:
		var list []string
		if err := node.Decode(&list); err != nil {
			return nil, err
		}
		return list, nil
	case yaml.MappingNode:
		var asMap map[string]any
		if err := node.Decode(&asMap); err != nil {
			return nil, err
		}
		list := make([]string, 0, len(asMap))
		for name := range asMap {
			list = append(list, name)
		}
		return list, nil
	default:
		return []string{}, nil
	}
}
