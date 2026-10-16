package agentwisp

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"syscall"
	"testing"
)

type recordingCommands struct {
	argv    []string
	failure error
}

func (c *recordingCommands) Output(name string, args ...string) ([]byte, error) {
	c.argv = append([]string{name}, args...)
	return nil, c.failure
}
func (c *recordingCommands) Interactive(_ io.Writer, name string, args ...string) error {
	c.argv = append([]string{name}, args...)
	return c.failure
}

func TestAppleRuntimePreservesArgv(t *testing.T) {
	c := &recordingCommands{}
	r := &appleRuntime{commands: c}
	command := []string{"claude", "a prompt with spaces; $(touch /tmp/never)", "--model", "example model"}
	req := RuntimeLaunch{Name: "agentwisp-work-test", Image: "custom:local", HostPath: "/project space/$(not-shell)", Command: command, EnvFile: "/private/env file"}
	if err := r.Launch(req, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []string{"container", "run", "-it", "--name", req.Name, "--label", managedLabel + "=true", "--label", workspaceLabel + "=" + req.HostPath, "--volume", req.HostPath + ":/workspace", "--workdir", "/workspace", "--env-file", req.EnvFile, req.Image}
	want = append(want, command...)
	if !reflect.DeepEqual(c.argv, want) {
		t.Fatalf("got %q want %q", c.argv, want)
	}
	req.Image = ""
	req.EnvFile = ""
	if err := r.Launch(req, io.Discard); err != nil {
		t.Fatal(err)
	}
	want = append([]string{"container", "exec", "-it", "--workdir", "/workspace", req.Name}, command...)
	if !reflect.DeepEqual(c.argv, want) {
		t.Fatalf("got %q want %q", c.argv, want)
	}
}
func TestAppleRuntimeErrorKinds(t *testing.T) {
	for _, tc := range []struct{ cause, kind error }{
		{exec.ErrNotFound, ErrRuntimeUnavailable},
		{&os.PathError{Op: "exec", Path: "container", Err: syscall.EACCES}, ErrRuntimePermission},
		{syscall.ECONNREFUSED, ErrRuntimeUnavailable},
		{errors.New("unrecognized CLI failure"), ErrRuntimeFailed},
		// A CLI message alone must not be misinterpreted as an absent workspace.
		{errors.New("not found"), ErrRuntimeFailed},
	} {
		r := &appleRuntime{commands: &recordingCommands{failure: fmt.Errorf("wrapped: %w", tc.cause)}}
		exists, err := r.Exists("agentwisp-work-test")
		if exists || !errors.Is(err, tc.kind) || !errors.Is(err, tc.cause) || errors.Is(err, ErrWorkspaceAbsent) {
			t.Fatalf("%v: %v", tc.cause, err)
		}
	}
}
func TestHostCommandsPreserveMissingCLICause(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := newAppleRuntime()
	_, err := r.List()
	if !errors.Is(err, ErrRuntimeUnavailable) || !errors.Is(err, exec.ErrNotFound) {
		t.Fatal(err)
	}
	err = r.Launch(RuntimeLaunch{Name: "agentwisp-work-test", Command: []string{"bash"}}, io.Discard)
	if !errors.Is(err, ErrRuntimeUnavailable) || !errors.Is(err, exec.ErrNotFound) {
		t.Fatal(err)
	}
}
