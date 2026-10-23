package agentwisp

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestAgentCommand(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		want := []string{provider, "--model", "example model", "$(touch /tmp/not-executed); echo hi"}
		got, err := agentCommand(append([]string{provider, "--"}, want[1:]...))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	for _, args := range [][]string{nil, {"bash"}, {"claude;id"}, {"/usr/bin/claude"}} {
		if _, err := agentCommand(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestMonitoredCommandPreservesArguments(t *testing.T) {
	// Execute the actual wrapper with a harmless replacement agent. Shell
	// metacharacters in a prompt must be printed literally, never evaluated.
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "must-not-exist")
	prompt := "$(touch " + shellQuote(sentinel) + "); 'quoted' $HOME"
	launch := []string{"sh", "-c", `printf '%s\n' "$1" "$HTTP_PROXY"`, "test-agent", prompt}
	args := monitoredCommand(monitorConfig{}, launch)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "AGENTWISP_PROXY_HOST=192.0.2.1", "AGENTWISP_MONITOR_TOKEN=test-token", "AGENTWISP_MONITOR_PORT=1234")
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != prompt+"\nhttp://agentwisp:test-token@192.0.2.1:1234\n" {
		t.Fatalf("changed arguments: %q", output)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("prompt executed as shell code")
	}
}

func TestAgentLaunchStartsStoppedWorkspace(t *testing.T) {
	name := "agentwisp-work-test"
	calls := fakeContainer(t, []Workspace{{Name: name, Path: "/project"}})
	path := filepath.Join(filepath.Dir(calls), "container")
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script = bytes.Replace(script, []byte("stop) ;;"), []byte("stop|start|exec) ;;"), 1)
	if err := os.WriteFile(path, script, 0o700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := defaultWorkspaceService().RunAgent(name, []string{"claude", "--continue"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(calls)
	text := string(data)
	launch := regexp.MustCompile(`exec -it --workdir /workspace ` + name + ` env AGENTWISP_SESSION=ses_[A-Z0-9]+ claude --settings \{.*\} --continue\n`)
	if !strings.Contains(text, "start "+name) || !launch.MatchString(text) {
		t.Fatalf("unexpected launch: %s", text)
	}
	if strings.Contains(text, "dangerously") {
		t.Fatal("permission bypass added")
	}
}

func TestAgentLaunchRequiresWorkspace(t *testing.T) {
	calls := fakeContainer(t, nil)
	err := defaultWorkspaceService().RunAgent("agentwisp-work-test", []string{"claude"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "agw add") {
		t.Fatalf("missing setup guidance: %v", err)
	}
	data, _ := os.ReadFile(calls)
	if strings.Contains(string(data), "exec") || strings.Contains(string(data), "run ") {
		t.Fatalf("unexpected mutation: %s", data)
	}
}

func TestInteractiveErrorsDoNotEchoArguments(t *testing.T) {
	err := execInteractive(&bytes.Buffer{}, "sh", "-c", "exit 7", "private-prompt-text")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "private-prompt-text") {
		t.Fatal("prompt leaked in error")
	}
}
