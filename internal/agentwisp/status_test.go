package agentwisp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Run the generated hook commands exactly as the providers would, then read
// the result back through the host parser.
func TestStatusHooksWriteReadableState(t *testing.T) {
	t.Setenv("AGENTWISP_STATE_DIR", t.TempDir())
	const workspace = "agentwisp-work-status"
	dir := agentStatusDir(workspace)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "AGENTWISP_STATUS_DIR="+dir)

	claude, err := withStatusHooks([]string{"claude", "--continue"})
	if err != nil || claude[1] != "--settings" || claude[len(claude)-1] != "--continue" {
		t.Fatalf("%q %v", claude, err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct{ Type, Command string }
		}
	}
	if err := json.Unmarshal([]byte(claude[2]), &settings); err != nil {
		t.Fatal(err)
	}
	for _, step := range [][2]string{{"UserPromptSubmit", "working"}, {"Notification", "needs-input"}, {"Stop", "idle"}} {
		event, state := step[0], step[1]
		command := settings.Hooks[event][0].Hooks[0].Command
		cmd := exec.Command("sh", "-c", command) // Claude runs hook commands through a shell.
		cmd.Env = append(env, "AGENTWISP_SESSION=ses_CLAUDE")
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"` + event + `"}`)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s hook: %v %s", event, err, output)
		}
		got := readAgentStatuses(workspace)
		if len(got) != 1 || got[0].State != state || got[0].Provider != "claude" || time.Since(got[0].Time) > time.Minute {
			t.Fatalf("%s: %+v", event, got)
		}
	}

	codex, err := withStatusHooks([]string{"codex", "resume"})
	if err != nil || codex[1] != "-c" || !strings.HasPrefix(codex[2], "notify=") || codex[3] != "resume" {
		t.Fatalf("%q %v", codex, err)
	}
	var notify []string // JSON array syntax is also a valid TOML array here.
	if err := json.Unmarshal([]byte(strings.TrimPrefix(codex[2], "notify=")), &notify); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(notify[0], append(notify[1:], `{"type":"agent-turn-complete"}`)...)
	cmd.Env = append(env, "AGENTWISP_SESSION=ses_CODEX")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("codex notify: %v %s", err, output)
	}
	if got := readAgentStatuses(workspace); len(got) != 2 || got[1].Provider != "codex" || got[1].State != "idle" {
		t.Fatalf("%+v", got)
	}
	if summary := agentSummary(readAgentStatuses(workspace)); summary != "idle" {
		t.Fatalf("summary %q", summary)
	}

	// A hook without an agw session (e.g. the agent's own shell) writes nothing.
	cmd = exec.Command(notify[0], notify[1:]...)
	cmd.Env = append(env, "AGENTWISP_SESSION=../../etc")
	_ = cmd.Run()
	if got := readAgentStatuses(workspace); len(got) != 2 {
		t.Fatalf("unexpected session file: %+v", got)
	}
}

func TestAgentStatusIsUntrusted(t *testing.T) {
	t.Setenv("AGENTWISP_STATE_DIR", t.TempDir())
	const workspace = "agentwisp-work-status"
	dir := agentStatusDir(workspace)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("claude-ses_A.json", `{"state":"needs-input","time":1}`)
	write("claude-ses_B.json", `{"state":"\u001b[2Jpwned","time":1}`)
	write("evil-ses_C.json", `{"state":"working","time":1}`)
	write("claude-ses_D.json", strings.Repeat(" ", agentStatusMaxBytes)+`{"state":"working"}`)
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "claude-ses_E.json")); err != nil {
		t.Fatal(err)
	}
	got := readAgentStatuses(workspace)
	if len(got) != 1 || got[0].Session != "ses_A" {
		t.Fatalf("accepted untrusted status: %+v", got)
	}
	write("claude-ses_F.json", `{"state":"needs-input","time":1}`)
	if summary := agentSummary(readAgentStatuses(workspace)); summary != "needs-input 2" {
		t.Fatalf("summary %q", summary)
	}
	clearAgentStatuses(workspace)
	if got := readAgentStatuses(workspace); len(got) != 0 {
		t.Fatalf("statuses kept after restart: %+v", got)
	}
}

func TestWatchReportsTransitionsOnly(t *testing.T) {
	t.Setenv("AGENTWISP_STATE_DIR", t.TempDir())
	s, r, _, _ := testService()
	r.items = []Workspace{{Name: "agentwisp-work-w", Running: true}, {Name: "agentwisp-work-off"}}
	for _, name := range []string{"agentwisp-work-w", "agentwisp-work-off"} {
		if err := os.MkdirAll(agentStatusDir(name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	session := AgentSession{ID: "ses_W", Provider: ProviderClaude}
	write := func(name, state string) {
		if err := writeHostAgentStatus(name, session, state); err != nil {
			t.Fatal(err)
		}
	}
	write("agentwisp-work-w", "working")
	write("agentwisp-work-off", "needs-input") // Stopped workspace: ignored.

	previous := agentStatusPoll
	agentStatusPoll = 20 * time.Millisecond
	defer func() { agentStatusPoll = previous }()
	stop := make(chan struct{})
	out := &lockedBuffer{}
	done := make(chan error)
	go func() { done <- s.Watch(false, out, stop) }()
	time.Sleep(60 * time.Millisecond)
	write("agentwisp-work-w", "needs-input")
	time.Sleep(80 * time.Millisecond)
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Count(text, "needs input") != 1 || !strings.Contains(text, "agentwisp-work-w: claude needs input") || strings.Contains(text, "agentwisp-work-off") {
		t.Fatalf("unexpected watch output:\n%s", text)
	}
}

type lockedBuffer struct {
	mu   sync.Mutex
	data strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}
func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}
