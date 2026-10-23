package agentwisp

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const defaultWorkspaceImage = "agentwisp-agents:local"

type Monitor interface {
	Disable(string) error
	Prepare(RuntimeLaunch) (RuntimeLaunch, func(), error)
}

// WorkspaceService owns lifecycle policy shared by CLI and TUI. There is no
// mutable global runtime. Create/start/stop/remove of one workspace are
// serialized across processes; interactive sessions run outside the lock.
// Lock order is lifecycle, then monitor.
type WorkspaceService struct {
	runtime Runtime
	monitor Monitor
	lock    func(string) (func(), error)
}

func NewWorkspaceService(runtime Runtime, monitor Monitor) *WorkspaceService {
	return &WorkspaceService{runtime: runtime, monitor: monitor, lock: lockWorkspaceLifecycle}
}
func defaultWorkspaceService() *WorkspaceService {
	runtime := newAppleRuntime()
	return NewWorkspaceService(runtime, workspaceMonitor{addresses: runtime.Addresses})
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
	unlock, err := s.lock(name)
	if err != nil {
		return err
	}
	defer unlock()
	// Re-check under the lock: another process may have removed it meanwhile.
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
	unlock, err := s.lock(name)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.runtime.Stop(name); err != nil {
		return err
	}
	if err := s.monitor.Disable(name); err != nil {
		return fmt.Errorf("workspace stopped, but proxy shutdown failed: %w", err)
	}
	return nil
}

// startLocked starts a stopped workspace while holding the lifecycle lock and
// releases it before any interactive session begins.
func (s *WorkspaceService) startLocked(name string, out io.Writer) error {
	unlock, err := s.lock(name)
	if err != nil {
		return err
	}
	defer unlock()
	return s.ensureStarted(name, out)
}
func (s *WorkspaceService) ensureStarted(name string, out io.Writer) error {
	running, err := s.runtime.Running(name)
	if err != nil {
		return err
	}
	if !running {
		fmt.Fprintf(out, "Starting %s...\n", name)
		if err := s.runtime.Start(name); err != nil {
			return err
		}
		clearAgentStatuses(name) // No session survives a container restart.
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
	if err := s.ensure(name, cwd, out, warnWritableGit); err != nil {
		return err
	}
	return s.launch(RuntimeLaunch{Name: name, Command: []string{"bash"}}, errOut)
}

// Ensure creates the workspace for cwd or starts it, without entering it.
// The existence check and creation happen under one lock, so two concurrent
// `agw add` runs cannot both try to create the container.
func (s *WorkspaceService) Ensure(name, cwd string, out io.Writer) error {
	return s.ensure(name, cwd, out, nil)
}
func (s *WorkspaceService) ensure(name, cwd string, out io.Writer, onCreate func(string, io.Writer)) error {
	if !validWorkspaceName(name) {
		return fmt.Errorf("invalid workspace name %q", name)
	}
	unlock, err := s.lock(name)
	if err != nil {
		return err
	}
	defer unlock()
	exists, err := s.runtime.Exists(name)
	if err != nil {
		return err
	}
	if exists {
		return s.ensureStarted(name, out)
	}
	image, err := workspaceImage()
	if err != nil {
		return err
	}
	if strings.ContainsAny(cwd, ":\r\n") {
		return fmt.Errorf("workspace path contains unsupported volume delimiter")
	}
	mounts, err := workspaceStateMounts(name)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Creating %s...\n", name)
	if onCreate != nil {
		onCreate(cwd, out)
	}
	return s.runtime.Create(RuntimeCreate{Name: name, Image: image, HostPath: cwd, Mounts: mounts})
}

// Container-side status directory written by agent hooks (see status.go).
const containerStatusDir = "/agentwisp/status"

// workspaceStateMounts prepares per-workspace host directories that outlive
// the container: agent auth/config (so recreating a workspace or changing its
// image keeps logins) and the agent status directory. They are separate per
// workspace, so one compromised workspace cannot read another's tokens. The
// monitor config and proxy token live beside them and are never mounted.
func workspaceStateMounts(name string) ([]RuntimeMount, error) {
	if err := ensureMonitorStateDir(name); err != nil {
		return nil, err
	}
	state := monitorStateDir(name)
	var mounts []RuntimeMount
	if os.Getenv("AGENTWISP_PERSIST_AUTH") != "0" {
		home := os.Getenv("AGENTWISP_AGENT_HOME")
		if home == "" {
			home = "/home/node"
		}
		if !filepath.IsAbs(home) || strings.ContainsAny(home, ":\r\n") {
			return nil, fmt.Errorf("AGENTWISP_AGENT_HOME must be an absolute container path")
		}
		for _, agent := range []string{"claude", "codex"} {
			mounts = append(mounts, RuntimeMount{Host: filepath.Join(state, "home", agent), Container: path.Join(home, "."+agent)})
		}
	}
	mounts = append(mounts, RuntimeMount{Host: filepath.Join(state, "status"), Container: containerStatusDir})
	for _, mount := range mounts {
		if err := ensurePrivateDir(filepath.Dir(mount.Host)); err != nil {
			return nil, err
		}
		if err := ensurePrivateDir(mount.Host); err != nil {
			return nil, err
		}
	}
	return mounts, nil
}

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace state must be a real directory: %s", dir)
	}
	return os.Chmod(dir, 0o700)
}

// PurgeState deletes data kept outside the container: saved agent logins and
// agent status. Traffic logs and monitor configuration are left alone.
func PurgeState(name string) error {
	if err := ensureMonitorStateDir(name); err != nil {
		return err
	}
	for _, dir := range []string{"home", "status"} {
		if err := os.RemoveAll(filepath.Join(monitorStateDir(name), dir)); err != nil {
			return err
		}
	}
	return nil
}
func (s *WorkspaceService) Enter(name string, out, errOut io.Writer) error {
	if err := s.require(name); err != nil {
		return err
	}
	if err := s.startLocked(name, out); err != nil {
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
