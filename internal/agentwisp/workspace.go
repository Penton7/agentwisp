package agentwisp

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const defaultWorkspaceImage = "agentwisp-agents:local"

type Monitor interface {
	Disable(string) error
	Prepare(RuntimeLaunch) (RuntimeLaunch, func(), error)
}

// WorkspaceService owns lifecycle policy shared by CLI and TUI. There is no
// mutable global runtime. Cross-process serialization is deferred to CORE-04.
type WorkspaceService struct {
	runtime Runtime
	monitor Monitor
}

func NewWorkspaceService(runtime Runtime, monitor Monitor) *WorkspaceService {
	return &WorkspaceService{runtime: runtime, monitor: monitor}
}
func defaultWorkspaceService() *WorkspaceService {
	return NewWorkspaceService(newAppleRuntime(), workspaceMonitor{})
}
func (s *WorkspaceService) List() ([]Workspace, error) { return s.runtime.List() }
func (s *WorkspaceService) require(name string) error {
	if !validWorkspaceName(name) {
		return fmt.Errorf("invalid workspace name %q", name)
	}
	exists, err := s.runtime.Exists(name)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: %s; create it with agw add first", ErrWorkspaceAbsent, name)
	}
	return nil
}
func (s *WorkspaceService) Remove(name string, force bool) error {
	if err := s.require(name); err != nil {
		return err
	}
	running, err := s.runtime.Running(name)
	if err != nil {
		return err
	}
	if running && !force {
		return fmt.Errorf("workspace %s is running; stop it first or use --force", name)
	}
	if err := s.monitor.Disable(name); err != nil {
		return fmt.Errorf("stop workspace proxy: %w", err)
	}
	return s.runtime.Remove(name, force)
}
func (s *WorkspaceService) Stop(name string) error {
	if err := s.require(name); err != nil {
		return err
	}
	if err := s.runtime.Stop(name); err != nil {
		return err
	}
	if err := s.monitor.Disable(name); err != nil {
		return fmt.Errorf("workspace stopped, but proxy shutdown failed: %w", err)
	}
	return nil
}
func (s *WorkspaceService) ensureStarted(name string, out io.Writer) error {
	running, err := s.runtime.Running(name)
	if err != nil {
		return err
	}
	if !running {
		fmt.Fprintf(out, "Starting %s...\n", name)
		return s.runtime.Start(name)
	}
	return nil
}
func (s *WorkspaceService) launch(req RuntimeLaunch, errOut io.Writer) error {
	prepared, cleanup, err := s.monitor.Prepare(req)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return err
	}
	return s.runtime.Launch(prepared, errOut)
}
func (s *WorkspaceService) Open(name, cwd string, out, errOut io.Writer) error {
	if !validWorkspaceName(name) {
		return fmt.Errorf("invalid workspace name %q", name)
	}
	exists, err := s.runtime.Exists(name)
	if err != nil {
		return err
	}
	if !exists {
		image, err := workspaceImage()
		if err != nil {
			return err
		}
		if strings.ContainsAny(cwd, ":\r\n") {
			return fmt.Errorf("workspace path contains unsupported volume delimiter")
		}
		return s.launch(RuntimeLaunch{Name: name, Image: image, HostPath: cwd, Command: []string{"bash"}}, errOut)
	}
	if err := s.ensureStarted(name, out); err != nil {
		return err
	}
	return s.launch(RuntimeLaunch{Name: name, Command: []string{"bash"}}, errOut)
}
func (s *WorkspaceService) Enter(name string, out, errOut io.Writer) error {
	if err := s.require(name); err != nil {
		return err
	}
	if err := s.ensureStarted(name, out); err != nil {
		return err
	}
	return s.launch(RuntimeLaunch{Name: name, Command: []string{"bash"}}, errOut)
}

// Shell retains the CLI's exec-only behavior; Enter starts stopped workspaces.
func (s *WorkspaceService) Shell(name string, errOut io.Writer) error {
	if err := s.require(name); err != nil {
		return err
	}
	return s.launch(RuntimeLaunch{Name: name, Command: []string{"bash"}}, errOut)
}

func workspaceImage() (string, error) {
	image := os.Getenv("AGENTWISP_IMAGE")
	if image == "" {
		return defaultWorkspaceImage, nil
	}
	if strings.HasPrefix(image, "-") || strings.ContainsAny(image, " \t\r\n") {
		return "", fmt.Errorf("invalid AGENTWISP_IMAGE")
	}
	return image, nil
}

func (s *WorkspaceService) Resolve(cwd string) (string, error) {
	preferred := workspaceContainerName(cwd)
	workspaces, err := s.List()
	if err != nil {
		return "", err
	}
	match := ""
	for _, item := range workspaces {
		path := item.Path
		if canonical, err := filepath.EvalSymlinks(path); err == nil {
			path = canonical
		}
		if filepath.IsAbs(path) && filepath.Clean(path) == cwd {
			if match != "" {
				return "", fmt.Errorf("multiple workspaces for %q; select one in the manager", cwd)
			}
			match = item.Name
		} else if item.Name == preferred {
			return "", fmt.Errorf("workspace %q belongs to a different or unknown path", preferred)
		}
	}
	if match != "" {
		return match, nil
	}
	return preferred, nil
}

func workspaceIndex(workspaces []Workspace, name string) int {
	for i, item := range workspaces {
		if item.Name == name {
			return i
		}
	}
	if len(workspaces) == 0 {
		return 0
	}
	return len(workspaces) - 1
}
