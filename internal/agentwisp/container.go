package agentwisp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

func listWorkspaces() ([]workspace, error) {
	all, err := containerIDs(true)
	if err != nil {
		return nil, err
	}
	runningIDs, err := containerIDs(false)
	if err != nil {
		return nil, err
	}

	running := make(map[string]bool, len(runningIDs))
	for _, id := range runningIDs {
		running[id] = true
	}

	workspaces := make([]workspace, 0, len(all))
	for _, name := range all {
		if !strings.HasPrefix(name, containerPrefix) &&
			!strings.HasPrefix(name, legacyContainerPrefix) {
			continue
		}

		path, err := inspectWorkspacePath(name)
		if err != nil {
			return nil, err
		}
		if path == "" {
			path = "-"
		}
		workspaces = append(workspaces, workspace{
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

func containerIDs(all bool) ([]string, error) {
	args := []string{"list"}
	if all {
		args = append(args, "--all")
	}
	args = append(args, "--quiet")

	output, err := commandOutput("container", args...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(output)), nil
}

func inspectWorkspacePath(name string) (string, error) {
	output, err := commandOutput("container", "inspect", name)
	if err != nil {
		return "", err
	}
	return workspacePathFromInspect(output)
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

func containerExists(name string) (bool, error) {
	cmd := exec.Command("container", "inspect", name)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return false, fmt.Errorf("container CLI not found")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, err
}

func resolveContainerName(preferred, legacy string) (string, error) {
	exists, err := containerExists(preferred)
	if err != nil {
		return "", err
	}
	if exists {
		return preferred, nil
	}

	legacyExists, err := containerExists(legacy)
	if err != nil {
		return "", err
	}
	if legacyExists {
		return legacy, nil
	}
	return preferred, nil
}

func containerRunning(name string) (bool, error) {
	ids, err := containerIDs(false)
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

func stopWorkspace(name string) error {
	_, err := commandOutput("container", "stop", name)
	return err
}
