package agentwisp

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
)

func (r *appleRuntime) List() ([]Workspace, error) {
	all, err := r.containerIDs(true)
	if err != nil {
		return nil, err
	}
	runningIDs, err := r.containerIDs(false)
	if err != nil {
		return nil, err
	}

	running := make(map[string]bool, len(runningIDs))
	for _, id := range runningIDs {
		running[id] = true
	}

	names := make([]string, 0, len(all))
	for _, name := range all {
		if validWorkspaceName(name) {
			names = append(names, name)
		}
	}
	paths := make(map[string]string, len(names))
	// Bound argv size while avoiding one process launch per workspace.
	for offset := 0; offset < len(names); offset += 64 {
		batch := names[offset:min(offset+64, len(names))]
		output, err := r.output(append([]string{"inspect"}, batch...)...)
		if err != nil {
			return nil, err
		}
		parsed, err := workspacePathsFromInspect(output)
		if err != nil {
			return nil, err
		}
		for _, name := range batch {
			path, ok := parsed[name]
			if !ok {
				return nil, fmt.Errorf("inspect omitted workspace %q", name)
			}
			paths[name] = path
		}
	}
	workspaces := make([]Workspace, 0, len(names))
	for _, name := range names {
		path := paths[name]
		if path == "" {
			path = "-"
		}
		workspaces = append(workspaces, Workspace{
			ID:      discoveredWorkspaceID(name),
			Name:    name,
			Path:    path,
			Running: running[name],
		})
	}

	sort.Slice(workspaces, func(i, j int) bool {
		if workspaces[i].Running != workspaces[j].Running {
			return workspaces[i].Running
		}
		return workspaces[i].Name < workspaces[j].Name
	})
	return workspaces, nil
}

func validWorkspaceName(name string) bool {
	return name == sanitize(name) &&
		((strings.HasPrefix(name, containerPrefix) && len(name) > len(containerPrefix)) ||
			(strings.HasPrefix(name, legacyContainerPrefix) && len(name) > len(legacyContainerPrefix)))
}

func workspacePathsFromInspect(data []byte) (map[string]string, error) {
	var records []json.RawMessage
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("parse container inspect: %w", err)
	}
	paths := make(map[string]string, len(records))
	for _, record := range records {
		var identity struct {
			ID            string `json:"id"`
			Configuration struct {
				ID string `json:"id"`
			} `json:"configuration"`
		}
		if err := json.Unmarshal(record, &identity); err != nil {
			return nil, err
		}
		name := identity.ID
		if name == "" {
			name = identity.Configuration.ID
		}
		if name == "" {
			return nil, fmt.Errorf("container inspect record has no ID")
		}
		if _, exists := paths[name]; exists {
			return nil, fmt.Errorf("duplicate container ID %q", name)
		}
		path, err := workspacePathFromInspect(record)
		if err != nil {
			return nil, err
		}
		paths[name] = path
	}
	return paths, nil
}

func (r *appleRuntime) containerIDs(all bool) ([]string, error) {
	args := []string{"list"}
	if all {
		args = append(args, "--all")
	}
	args = append(args, "--quiet")

	output, err := r.output(args...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(output)), nil
}

func workspacePathFromInspect(data []byte) (string, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return "", fmt.Errorf("parse container inspect output: %w", err)
	}

	if path := findLabel(value, workspaceLabel); path != "" {
		return path, nil
	}
	if path := findLabel(value, legacyWorkspaceLabel); path != "" {
		return path, nil
	}
	return findWorkspaceMount(value), nil
}

func findLabel(value any, label string) string {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if strings.EqualFold(key, label) {
				if text, ok := child.(string); ok {
					return text
				}
			}
			if strings.EqualFold(key, "labels") {
				if labels, ok := child.(map[string]any); ok {
					if text, ok := labels[label].(string); ok {
						return text
					}
				}
			}
			if found := findLabel(child, label); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range current {
			if found := findLabel(child, label); found != "" {
				return found
			}
		}
	}
	return ""
}

func findWorkspaceMount(value any) string {
	switch current := value.(type) {
	case map[string]any:
		target := mapString(current, "target", "destination", "dst")
		source := mapString(current, "source", "src")
		if target == "/workspace" && source != "" {
			return source
		}
		for _, child := range current {
			if found := findWorkspaceMount(child); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range current {
			if found := findWorkspaceMount(child); found != "" {
				return found
			}
		}
	case string:
		if source, ok := strings.CutSuffix(current, ":/workspace"); ok {
			return source
		}
	}
	return ""
}

func mapString(values map[string]any, keys ...string) string {
	for key, value := range values {
		for _, wanted := range keys {
			if strings.EqualFold(key, wanted) {
				if text, ok := value.(string); ok {
					return text
				}
			}
		}
	}
	return ""
}

func (r *appleRuntime) Exists(name string) (bool, error) {
	ids, err := r.containerIDs(true)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == name {
			return true, nil
		}
	}
	return false, nil
}

func (r *appleRuntime) Running(name string) (bool, error) {
	ids, err := r.containerIDs(false)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == name {
			return true, nil
		}
	}
	return false, nil
}

// Addresses returns the IPv4 network addresses (and gateways) reported for a
// container. Inspect output is untrusted: callers must only use these values
// to select one of the host's own interfaces, never as a bind or dial target.
func (r *appleRuntime) Addresses(name string) ([]net.IP, error) {
	if !validWorkspaceName(name) {
		return nil, fmt.Errorf("invalid workspace name %q", name)
	}
	output, err := r.output("inspect", name)
	if err != nil {
		return nil, err
	}
	return containerAddressesFromInspect(output)
}

func containerAddressesFromInspect(data []byte) ([]net.IP, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("parse container inspect output: %w", err)
	}
	var found []net.IP
	collectAddresses(value, &found)
	return found, nil
}

func collectAddresses(value any, found *[]net.IP) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			lower := strings.ToLower(key)
			if text, ok := child.(string); ok && (strings.Contains(lower, "address") || strings.Contains(lower, "gateway")) {
				if ip := parseIPOrCIDR(text); ip != nil {
					*found = append(*found, ip)
				}
				continue
			}
			collectAddresses(child, found)
		}
	case []any:
		for _, child := range current {
			collectAddresses(child, found)
		}
	}
}

func parseIPOrCIDR(text string) net.IP {
	if ip, _, err := net.ParseCIDR(text); err == nil {
		return ip.To4()
	}
	if ip := net.ParseIP(text); ip != nil {
		return ip.To4()
	}
	return nil
}
