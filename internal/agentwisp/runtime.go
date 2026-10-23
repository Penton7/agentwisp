package agentwisp

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
)

// Runtime owns container operations; callers never build runtime CLI flags.
type Runtime interface {
	List() ([]Workspace, error)
	Exists(string) (bool, error)
	Running(string) (bool, error)
	Start(string) error
	Stop(string) error
	Remove(string, bool) error
	Create(RuntimeCreate) error
	Addresses(string) ([]net.IP, error)
	CheckAgent(string, string) error
	Launch(RuntimeLaunch, io.Writer) error
}

// RuntimeCreate describes a new detached workspace container. Its main process
// is a keep-alive loop, so interactive shells and agents are always exec
// sessions: exiting one of them never stops the container under the others.
type RuntimeCreate struct {
	Name     string
	Image    string
	HostPath string
}

// RuntimeLaunch is an interactive exec session in an existing container.
type RuntimeLaunch struct {
	Name    string
	Command []string
	EnvFile string
}

// keepAliveScript is PID 1 of new containers. The trap lets `container stop`
// finish promptly: PID 1 ignores SIGTERM unless it installs a handler.
const keepAliveScript = `trap 'exit 0' TERM INT; while :; do sleep 86400 & wait $!; done`

var (
	ErrWorkspaceAbsent    = errors.New("workspace absent")
	ErrRuntimeUnavailable = errors.New("runtime unavailable")
	ErrRuntimePermission  = errors.New("runtime permission denied")
	ErrRuntimeFailed      = errors.New("runtime operation failed")
)

// Unknown CLI failures stay Failed: human stderr is not a reliable error schema.
type RuntimeError struct {
	Op    string
	Kind  error
	Cause error
}

func (e *RuntimeError) Error() string        { return fmt.Sprintf("%s: %v: %v", e.Op, e.Kind, e.Cause) }
func (e *RuntimeError) Unwrap() error        { return e.Cause }
func (e *RuntimeError) Is(target error) bool { return target == e.Kind }
func runtimeError(op string, err error) error {
	if err == nil {
		return nil
	}
	kind := ErrRuntimeFailed
	switch {
	case errors.Is(err, os.ErrPermission):
		kind = ErrRuntimePermission
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ENOTCONN):
		kind = ErrRuntimeUnavailable
	}
	return &RuntimeError{Op: op, Kind: kind, Cause: err}
}

type runtimeCommands interface {
	Output(string, ...string) ([]byte, error)
	Interactive(io.Writer, string, ...string) error
}
type hostCommands struct{}

func (hostCommands) Output(name string, args ...string) ([]byte, error) {
	return commandOutput(name, args...)
}
func (hostCommands) Interactive(out io.Writer, name string, args ...string) error {
	return execInteractive(out, name, args...)
}

type appleRuntime struct{ commands runtimeCommands }

func newAppleRuntime() *appleRuntime { return &appleRuntime{commands: hostCommands{}} }
func (r *appleRuntime) output(args ...string) ([]byte, error) {
	data, err := r.commands.Output("container", args...)
	return data, runtimeError(args[0], err)
}
func (r *appleRuntime) Start(name string) error { _, err := r.output("start", name); return err }
func (r *appleRuntime) Stop(name string) error  { _, err := r.output("stop", name); return err }
func (r *appleRuntime) Remove(name string, force bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "--force")
	}
	_, err := r.output(append(args, name)...)
	return err
}
func (r *appleRuntime) CheckAgent(name, executable string) error {
	_, err := r.output("exec", name, "sh", "-c", `command -v "$1" >/dev/null`, "agw-check", executable)
	return err
}
func (r *appleRuntime) Create(req RuntimeCreate) error {
	if !validWorkspaceName(req.Name) || req.Image == "" || req.HostPath == "" {
		return fmt.Errorf("invalid runtime create")
	}
	_, err := r.output("run", "--detach", "--name", req.Name,
		"--label", managedLabel+"=true", "--label", workspaceLabel+"="+req.HostPath,
		"--volume", req.HostPath+":/workspace", "--workdir", "/workspace",
		req.Image, "sh", "-c", keepAliveScript)
	return err
}
func (r *appleRuntime) Launch(req RuntimeLaunch, errOut io.Writer) error {
	if !validWorkspaceName(req.Name) || len(req.Command) == 0 {
		return fmt.Errorf("invalid runtime launch")
	}
	args := []string{"exec", "-it", "--workdir", "/workspace"}
	if req.EnvFile != "" {
		args = append(args, "--env-file", req.EnvFile)
	}
	args = append(args, req.Name)
	args = append(args, req.Command...)
	return runtimeError(args[0], r.commands.Interactive(errOut, "container", args...))
}
