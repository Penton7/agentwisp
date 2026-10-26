package agentwisp

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Protected project paths: files that tools on the host execute or load as
// configuration (git, IDEs, agents, hook managers, direnv). Code in the
// container could otherwise plant a command that runs on the host the next
// time PyCharm, git or Claude Code there touches the project. Existing ones
// are mounted read-only over the writable project mount; the agent can read
// them but not change them, and cannot replace them (they are mount points).
var protectedProjectPaths = []string{
	".git",                    // config (fsmonitor, pager, hooksPath...), hooks, info/attributes
	".idea",                   // JetBrains run configurations, external tools, file watchers
	".vscode",                 // tasks, launch configurations, settings
	".claude",                 // Claude Code project hooks and permissions
	".mcp.json",               // MCP servers started by agents on the host
	".codex",                  // Codex project configuration
	".husky",                  // git hooks run by the host on commit
	".githooks",               // common core.hooksPath target
	".pre-commit-config.yaml", // pre-commit runs its entries on host commits
	".envrc",                  // direnv
}

// workspaceProtection says which protected paths a new workspace mounts
// read-only. Task clones keep .git writable: the agent commits there and
// agw task fetch reads it as data only.
func workspaceProtection(project string, task bool, getenv func(string) string) (mounts []RuntimeMount, protected, notes []string, err error) {
	writable := map[string]bool{}
	if task {
		writable[".git"] = true
	}
	if value := strings.TrimSpace(getenv("AGENTWISP_WRITABLE")); value != "" {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if !isProtectedPath(item) {
				return nil, nil, nil, fmt.Errorf("AGENTWISP_WRITABLE: %q is not a protected path (%s)", item, strings.Join(protectedProjectPaths, ", "))
			}
			writable[item] = true
		}
	}
	candidates := protectedProjectPaths
	if !writable[".git"] {
		// A read-only .git still runs hooks from wherever core.hooksPath
		// points; inside the project that is agent-writable too.
		candidates = append(append([]string{}, candidates...), workTreeHooks(project)...)
	}
	for _, name := range candidates {
		if writable[name] {
			continue
		}
		host := filepath.Join(project, name)
		info, err := os.Lstat(host)
		if err != nil {
			continue // Absent: nothing to protect; doctor reports it if it appears later.
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Mounting would expose the link target; leave it and say so.
			notes = append(notes, name+" is a symlink and is not protected")
			continue
		}
		if strings.ContainsAny(host, ",\r\n") {
			return nil, nil, nil, fmt.Errorf("cannot protect %s: path contains a comma; rename the project directory or set AGENTWISP_WRITABLE=%s", host, name)
		}
		mounts = append(mounts, RuntimeMount{Host: host, Container: path.Join("/workspace", name), ReadOnly: true})
		protected = append(protected, name)
	}
	return mounts, protected, notes, nil
}

// splitFileMounts separates protected files from directories.
func splitFileMounts(mounts []RuntimeMount, names []string) (dirs []RuntimeMount, dirNames, files []string) {
	for i, mount := range mounts {
		if info, err := os.Stat(mount.Host); err == nil && !info.IsDir() {
			files = append(files, names[i])
			continue
		}
		dirs = append(dirs, mount)
		dirNames = append(dirNames, names[i])
	}
	return dirs, dirNames, files
}

func isProtectedPath(name string) bool {
	for _, item := range protectedProjectPaths {
		if item == name {
			return true
		}
	}
	return false
}

// verifyProtection checks from inside the new container that every protected
// path is really read-only. The runtime's mount semantics are not something
// agw controls, so a workspace whose protection silently failed is removed
// instead of being handed to an agent.
func (s *WorkspaceService) verifyProtection(name string, protected []string) error {
	if len(protected) == 0 {
		return nil
	}
	const script = `for p do if [ ! -e "/workspace/$p" ] || [ -w "/workspace/$p" ]; then printf '%s\n' "$p"; fi; done`
	var out bytes.Buffer
	err := s.runtime.Exec(name, append([]string{"sh", "-c", script, "sh"}, protected...), &limitedWriter{w: &out, n: 4096})
	if err == nil && strings.TrimSpace(out.String()) == "" {
		return nil
	}
	reason := "check failed: " + fmt.Sprint(err)
	if err == nil {
		reason = "still writable: " + strings.Join(strings.Fields(out.String()), ", ")
	}
	if removeErr := s.runtime.Remove(name, true); removeErr != nil {
		return fmt.Errorf("read-only protection of %s did not take effect (%s), and removing the workspace failed: %v; remove it with `container rm --force %s`", name, reason, removeErr, name)
	}
	return fmt.Errorf("read-only protection did not take effect (%s); the workspace was removed. To continue without it, set AGENTWISP_WRITABLE=%s", reason, strings.Join(protected, ","))
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		chunk := p[:min(len(p), l.n)]
		l.n -= len(chunk)
		if _, err := l.w.Write(chunk); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// printProtection tells the user what changed for them at creation time.
func printProtection(project string, task bool, protected, notes []string, out io.Writer) {
	if len(protected) > 0 {
		fmt.Fprintf(out, "Read-only in the workspace (host tools run them): %s\n", strings.Join(protected, ", "))
		for _, name := range protected {
			if name == ".git" {
				fmt.Fprintln(out, "The agent can read history and diffs but cannot commit there; commit on the host.")
			}
		}
	}
	for _, note := range notes {
		fmt.Fprintln(out, "Note: "+note)
	}
	if task {
		return
	}
	if _, err := os.Lstat(filepath.Join(project, ".git")); err == nil && !contains(protected, ".git") {
		fmt.Fprintln(out, "Warning: this project's .git is writable inside the workspace. Code in the container")
		fmt.Fprintln(out, "can make later host git commands run arbitrary programs (config, hooks).")
	}
}

// workTreeHooks lists hook directories inside the project that host git
// runs from, e.g. core.hooksPath=scripts/hooks (relative to the work tree).
func workTreeHooks(project string) []string {
	var found []string
	data, err := readLimitedNoFollow(filepath.Join(project, ".git", "config"), 1<<20)
	if err != nil {
		return nil
	}
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = ""
			if fields := strings.Fields(strings.Trim(line, "[]")); len(fields) > 0 {
				section = strings.ToLower(fields[0])
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section != "core" || !strings.EqualFold(strings.TrimSpace(key), "hookspath") {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"")
		if value == "" || filepath.IsAbs(value) || strings.HasPrefix(value, "~") {
			continue
		}
		rel := filepath.ToSlash(filepath.Clean(value))
		top, _, _ := strings.Cut(rel, "/")
		if rel == "." || top == ".." || isProtectedPath(top) || contains(found, rel) {
			continue // Outside the project, or covered by a protected path.
		}
		found = append(found, rel)
	}
	sort.Strings(found)
	return found
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
