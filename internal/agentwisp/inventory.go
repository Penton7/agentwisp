package agentwisp

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const inventorySchemaVersion = 1

// Deliberately project an allowlist, rather than serializing runtime inspect,
// monitor configuration, or domain objects whose fields can grow independently.
type workspaceJSON struct {
	ID             WorkspaceID `json:"id"`
	ContainerName  string      `json:"container_name"`
	HostPath       *string     `json:"host_path"`
	ContainerState string      `json:"container_state"`
}
type workspaceListJSON struct {
	SchemaVersion int             `json:"schema_version"`
	Workspaces    []workspaceJSON `json:"workspaces"`
}
type workspaceInspectJSON struct {
	SchemaVersion int           `json:"schema_version"`
	Workspace     workspaceJSON `json:"workspace"`
}

func workspaceView(w Workspace) workspaceJSON {
	v := workspaceJSON{ID: w.ID, ContainerName: w.Name, ContainerState: "stopped"}
	if w.Running {
		v.ContainerState = "running"
	}
	if w.Path != "" && w.Path != "-" {
		path := w.Path
		v.HostPath = &path
	}
	return v
}
func writeWorkspaceListJSON(service *WorkspaceService, out io.Writer) error {
	items, err := service.List()
	if err != nil {
		return err
	}
	result := workspaceListJSON{SchemaVersion: inventorySchemaVersion, Workspaces: make([]workspaceJSON, 0, len(items))}
	for _, item := range items {
		result.Workspaces = append(result.Workspaces, workspaceView(item))
	}
	return json.NewEncoder(out).Encode(result)
}
func writeWorkspaceInspectJSON(service *WorkspaceService, id WorkspaceID, out io.Writer) error {
	item, err := service.Inspect(id)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(workspaceInspectJSON{SchemaVersion: inventorySchemaVersion, Workspace: workspaceView(item)})
}
func inspectJSONArgs(args []string) (WorkspaceID, error) {
	var id string
	jsonFlag := false
	for _, arg := range args {
		switch {
		case arg == "--json" && !jsonFlag:
			jsonFlag = true
		case arg != "" && !strings.HasPrefix(arg, "-") && id == "":
			id = arg
		default:
			return "", fmt.Errorf("usage: agw inspect <id> --json")
		}
	}
	if id == "" || !jsonFlag {
		return "", fmt.Errorf("usage: agw inspect <id> --json")
	}
	return WorkspaceID(id), nil
}

// Inspect resolves an exact AgentWisp ID through inventory, never as a runtime
// name or shell argument. Unknown and ambiguous identities fail closed.
func (s *WorkspaceService) Inspect(id WorkspaceID) (Workspace, error) {
	if id == "" {
		return Workspace{}, fmt.Errorf("workspace ID is required")
	}
	items, err := s.List()
	if err != nil {
		return Workspace{}, err
	}
	var selected Workspace
	found := false
	for _, item := range items {
		if item.ID != id {
			continue
		}
		if found {
			return Workspace{}, fmt.Errorf("ambiguous workspace ID %q", id)
		}
		selected = item
		found = true
	}
	if !found {
		return Workspace{}, fmt.Errorf("%w: ID %q", ErrWorkspaceAbsent, id)
	}
	return selected, nil
}
