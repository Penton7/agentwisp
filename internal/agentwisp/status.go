package agentwisp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Agent status without a session broker: `agw agent` installs provider hooks
// that write one small JSON file per session into the workspace's mounted
// status directory. Everything in that directory is written by code inside
// the container, so it is untrusted: size-limited, allowlisted and never used
// for anything but display.

const (
	agentStatusMaxBytes = 4096
	agentStatusMaxFiles = 64
)

// Variable so tests can poll quickly.
var agentStatusPoll = 2 * time.Second

var agentStates = map[string]int{"needs-input": 4, "working": 3, "starting": 2, "idle": 1}

type agentStatus struct {
	Provider string
	Session  string
	State    string
	Time     time.Time
}

func agentStatusDir(workspace string) string {
	return filepath.Join(monitorStateDir(workspace), "status")
}

// statusHookScript runs inside the container. $1 is the state; the session ID
// comes from the environment set by agw, the provider from $0.
const statusHookScript = `d="${AGENTWISP_STATUS_DIR:-` + containerStatusDir + `}"; [ -d "$d" ] || exit 0; ` +
	`case "$AGENTWISP_SESSION" in ses_*) ;; *) exit 0;; esac; ` +
	`f="$d/$0-$AGENTWISP_SESSION.json"; t="$f.tmp.$$"; ` +
	`printf '{"state":"%s","time":%s}\n' "$1" "$(date +%s)" > "$t" && mv -f "$t" "$f"; ` +
	`cat >/dev/null 2>&1 || true`

// withStatusHooks adds provider-specific hook configuration in front of the
// user's arguments. User settings files are not modified.
func withStatusHooks(command []string) ([]string, error) {
	if len(command) == 0 {
		return command, nil
	}
	provider := command[0]
	rest := command[1:]
	switch AgentProvider(provider) {
	case ProviderClaude:
		hook := func(state string) []map[string]any {
			return []map[string]any{{"hooks": []map[string]any{{
				"type":    "command",
				"command": "sh -c " + shellQuote(statusHookScript) + " claude " + state,
			}}}}
		}
		settings, err := json.Marshal(map[string]any{"hooks": map[string]any{
			"UserPromptSubmit": hook("working"),
			"Notification":     hook("needs-input"),
			"Stop":             hook("idle"),
		}})
		if err != nil {
			return nil, err
		}
		return append([]string{provider, "--settings", string(settings)}, rest...), nil
	case ProviderCodex:
		// Codex runs notify after each completed turn with a JSON argument.
		notify, err := json.Marshal([]string{"sh", "-c", statusHookScript, "codex", "idle"})
		if err != nil {
			return nil, err
		}
		return append([]string{provider, "-c", "notify=" + string(notify)}, rest...), nil
	}
	return command, nil
}

func agentStatusPath(workspace string, session AgentSession) string {
	return filepath.Join(agentStatusDir(workspace), string(session.Provider)+"-"+string(session.ID)+".json")
}

// writeHostAgentStatus records host-observed transitions (start). It goes
// through a temp file and rename so readers never see partial JSON.
func writeHostAgentStatus(workspace string, session AgentSession, state string) error {
	dir := agentStatusDir(workspace)
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return nil // Workspace created before status support: nothing to do.
	}
	file, err := os.CreateTemp(dir, ".host-*.tmp")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(file, "{\"state\":%q,\"time\":%d}\n", state, time.Now().Unix())
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), agentStatusPath(workspace, session))
	}
	if err != nil {
		_ = os.Remove(file.Name())
	}
	return err
}

func readAgentStatuses(workspace string) []agentStatus {
	dir := agentStatusDir(workspace)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var statuses []agentStatus
	if len(entries) > agentStatusMaxFiles {
		entries = entries[:agentStatusMaxFiles]
	}
	for _, entry := range entries {
		base, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !entry.Type().IsRegular() {
			continue
		}
		provider, session, ok := strings.Cut(base, "-")
		if !AgentProvider(provider).valid() || !strings.HasPrefix(session, "ses_") {
			continue
		}
		data, err := readLimitedNoFollow(filepath.Join(dir, entry.Name()), agentStatusMaxBytes)
		if err != nil {
			continue
		}
		var record struct {
			State string `json:"state"`
			Time  int64  `json:"time"`
		}
		if json.Unmarshal(data, &record) != nil || agentStates[record.State] == 0 {
			continue
		}
		statuses = append(statuses, agentStatus{Provider: provider, Session: session, State: record.State, Time: time.Unix(record.Time, 0)})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Session < statuses[j].Session })
	return statuses
}

func readLimitedNoFollow(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return io.ReadAll(io.LimitReader(file, limit))
}

// agentSummary is the most urgent state across a workspace's sessions.
func agentSummary(statuses []agentStatus) string {
	best := ""
	count := 0
	for _, status := range statuses {
		if agentStates[status.State] > agentStates[best] {
			best = status.State
		}
		if status.State == "needs-input" {
			count++
		}
	}
	if count > 1 {
		return best + " " + strconv.Itoa(count)
	}
	return best
}

// clearAgentStatuses drops records of sessions that cannot be alive, e.g.
// after the container was (re)started.
func clearAgentStatuses(workspace string) {
	entries, err := os.ReadDir(agentStatusDir(workspace))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			_ = os.Remove(filepath.Join(agentStatusDir(workspace), entry.Name()))
		}
	}
}

// workspaceAgentSummaries returns display states for running workspaces.
func workspaceAgentSummaries(workspaces []Workspace) map[string]string {
	summaries := make(map[string]string, len(workspaces))
	for _, item := range workspaces {
		if item.Running {
			summaries[item.Name] = agentSummary(readAgentStatuses(item.Name))
		}
	}
	return summaries
}

// Watch prints and optionally notifies about sessions that start waiting for
// the user or finish a turn. Notification text is built only from sanitized
// workspace names and allowlisted states, and passed to osascript as argv.
func (s *WorkspaceService) Watch(notify bool, out io.Writer, stop <-chan struct{}) error {
	previous := map[string]string{}
	first := true
	ticker := time.NewTicker(agentStatusPoll)
	defer ticker.Stop()
	for {
		workspaces, err := s.List()
		if err != nil {
			return err
		}
		current := map[string]string{}
		for _, item := range workspaces {
			if !item.Running {
				continue
			}
			for _, status := range readAgentStatuses(item.Name) {
				key := item.Name + "/" + status.Session
				current[key] = status.State
				if first || previous[key] == status.State || (status.State != "needs-input" && status.State != "idle") {
					continue
				}
				message := fmt.Sprintf("%s: %s %s", item.Name, status.Provider, map[string]string{"needs-input": "needs input", "idle": "finished a turn"}[status.State])
				fmt.Fprintf(out, "%s %s\n", time.Now().Format("15:04:05"), message)
				if notify {
					_ = desktopNotify("agw", message)
				}
			}
		}
		previous, first = current, false
		select {
		case <-stop:
			return nil
		case <-ticker.C:
		}
	}
}

func desktopNotify(title, message string) error {
	if _, err := exec.LookPath("osascript"); err != nil {
		return err
	}
	return exec.Command("osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, message).Run()
}
