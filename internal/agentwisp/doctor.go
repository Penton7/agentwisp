package agentwisp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Workspace layout: what a container gets at creation time and cannot gain
// later (main process, mounts, labels). Bump currentWorkspaceLayout whenever
// creation changes in a way that requires recreating older workspaces, and
// describe the change in workspaceLayoutHistory; doctor reports it to users.
const currentWorkspaceLayout = 3

var workspaceLayoutHistory = map[int]string{
	1: "interactive bash as the container's main process; no saved logins or agent status",
	2: "keep-alive main process (exiting a shell no longer stops agents), saved agent logins, agent status mount, layout label",
	3: "project .git, IDE, agent and hook configuration mounted read-only (host tools cannot be made to run agent-planted commands)",
}

// Doctor checks. Every check has a stable ID documented in docs/doctor.md;
// tests keep the registry, the code and the documentation in sync.
const (
	checkRuntime     = "runtime.available"
	checkGit         = "git.available"
	checkState       = "state.permissions"
	checkLayout      = "workspace.layout"
	checkPath        = "workspace.path"
	checkGitExec     = "workspace.git-exec"
	checkGitShared   = "workspace.git-shared"
	checkMonitorBind = "monitor.bind"
	checkMonitorUp   = "monitor.health"
	checkTask        = "task.state"
	checkProtected   = "workspace.protected"
)

var doctorChecks = []string{checkRuntime, checkGit, checkState, checkLayout, checkPath,
	checkGitExec, checkGitShared, checkProtected, checkMonitorBind, checkMonitorUp, checkTask}

const (
	levelOK   = "ok"
	levelWarn = "warn"
	levelFail = "fail"
)

type doctorFinding struct {
	Check   string `json:"check"`
	Level   string `json:"level"`
	Subject string `json:"subject"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

type doctorReport struct {
	SchemaVersion int             `json:"schema_version"`
	Layout        int             `json:"current_layout"`
	Findings      []doctorFinding `json:"findings"`
}

func (r *doctorReport) add(check, level, subject, message, fix string) {
	r.Findings = append(r.Findings, doctorFinding{Check: check, Level: level, Subject: subject, Message: message, Fix: fix})
}

func (r *doctorReport) count(level string) int {
	n := 0
	for _, f := range r.Findings {
		if f.Level == level {
			n++
		}
	}
	return n
}

var errDoctorProblems = errors.New("doctor found problems")

func handleDoctorCommand(service *WorkspaceService, args []string, out io.Writer) error {
	jsonOutput, strict := false, false
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOutput = true
		case "--strict":
			strict = true
		default:
			return fmt.Errorf("usage: agw doctor [--json] [--strict]")
		}
	}
	report := service.Doctor()
	if jsonOutput {
		if err := json.NewEncoder(out).Encode(report); err != nil {
			return err
		}
	} else {
		printDoctorReport(report, out)
	}
	if report.count(levelFail) > 0 || (strict && report.count(levelWarn) > 0) {
		return errDoctorProblems
	}
	return nil
}

func printDoctorReport(report doctorReport, out io.Writer) {
	for _, f := range report.Findings {
		fmt.Fprintf(out, "%-4s  %-22s %s: %s\n", strings.ToUpper(f.Level), f.Check, terminalText(f.Subject), terminalText(f.Message))
		if f.Fix != "" {
			fmt.Fprintf(out, "      fix: %s\n", terminalText(f.Fix))
		}
	}
	fmt.Fprintf(out, "\n%d ok, %d warning(s), %d problem(s). Current workspace layout: %d.\n",
		report.count(levelOK), report.count(levelWarn), report.count(levelFail), report.Layout)
}

// Doctor is read-only: it never runs git, never starts containers or proxies,
// and never changes files. Findings are advice; the user decides.
func (s *WorkspaceService) Doctor() doctorReport {
	report := doctorReport{SchemaVersion: 1, Layout: currentWorkspaceLayout}
	checkHostState(&report)
	if _, err := exec.LookPath("git"); err != nil {
		report.add(checkGit, levelWarn, "host", "git not found; agw task is unavailable", "install Xcode command line tools or git")
	} else {
		report.add(checkGit, levelOK, "host", "git available", "")
	}
	tasks := loadAllTasks(&report)

	workspaces, err := s.List()
	if err != nil {
		report.add(checkRuntime, levelFail, "host", "cannot list workspaces: "+err.Error(), "install/start Apple container (`container system start`)")
		return report
	}
	report.add(checkRuntime, levelOK, "host", fmt.Sprintf("%d workspace(s)", len(workspaces)), "")
	for _, item := range workspaces {
		checkWorkspace(&report, item, tasks)
	}
	return report
}

func checkHostState(report *doctorReport) {
	root := agentWispStateDir()
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		report.add(checkState, levelOK, "host", "no state directory yet", "")
		return
	}
	if err != nil || !info.IsDir() {
		report.add(checkState, levelFail, "host", root+" is not a real directory", "move it aside; AgentWisp recreates it")
		return
	}
	var loose []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			loose = append(loose, path)
		}
		// Contents of mounted agent directories belong to the container.
		if entry.IsDir() && (entry.Name() == "home" || entry.Name() == "status") && path != root {
			return filepath.SkipDir
		}
		return nil
	})
	if len(loose) > 0 {
		sort.Strings(loose)
		report.add(checkState, levelFail, "host", fmt.Sprintf("%d state path(s) are symlinks or readable by others, e.g. %s", len(loose), loose[0]),
			"chmod -R go-rwx "+shellQuote(root)+" and remove unexpected symlinks")
		return
	}
	report.add(checkState, levelOK, "host", "state directory is private", "")
}

func loadAllTasks(report *doctorReport) map[string]taskRecord {
	tasks := map[string]taskRecord{}
	dirs, _ := filepath.Glob(filepath.Join(agentWispStateDir(), "tasks", "*", "*.json"))
	for _, file := range dirs {
		data, err := readLimitedNoFollow(file, 64<<10)
		var task taskRecord
		if err == nil {
			err = json.Unmarshal(data, &task)
		}
		if err == nil {
			_, err = loadTask(task.Repo, task.Name) // Validates consistency.
		}
		if err != nil {
			report.add(checkTask, levelWarn, file, "unreadable task record: "+err.Error(), "remove the record if the task is gone")
			continue
		}
		if _, err := os.Stat(task.Path); err != nil {
			report.add(checkTask, levelWarn, task.Name, "task clone is missing: "+task.Path, "agw task rm "+task.Name+" --force (from "+task.Repo+")")
		}
		tasks[task.Path] = task
	}
	return tasks
}

func checkWorkspace(report *doctorReport, item Workspace, tasks map[string]taskRecord) {
	name := item.Name
	layout := max(item.Layout, 1)
	if layout < currentWorkspaceLayout {
		var missing []string
		for v := layout + 1; v <= currentWorkspaceLayout; v++ {
			missing = append(missing, fmt.Sprintf("v%d: %s", v, workspaceLayoutHistory[v]))
		}
		report.add(checkLayout, levelWarn, name, fmt.Sprintf("created with layout %d; missing %s", layout, strings.Join(missing, "; ")),
			"recreate: export container-only data, `agw rm --force` in the project, then `agw add` or `agw task new` (README: Upgrading)")
	} else if layout > currentWorkspaceLayout {
		report.add(checkLayout, levelWarn, name, fmt.Sprintf("created by a newer AgentWisp (layout %d)", layout), "upgrade agw")
	} else {
		report.add(checkLayout, levelOK, name, "current layout", "")
	}

	path := item.Path
	if path == "" || path == "-" || !filepath.IsAbs(path) {
		report.add(checkPath, levelWarn, name, "host path unknown", "")
	} else if info, err := os.Stat(path); err != nil || !info.IsDir() {
		report.add(checkPath, levelWarn, name, "host path is missing: "+path, "remove the workspace if the project is gone")
	} else {
		checkMountedGit(report, item, tasks)
		checkProtectedPaths(report, item, tasks)
	}
	checkWorkspaceMonitor(report, name)
}

// checkMountedGit inspects a mounted .git without running git. Anything that
// makes host git execute programs is reported: workspace code could have
// written it.
func checkMountedGit(report *doctorReport, item Workspace, tasks map[string]taskRecord) {
	name, path := item.Name, item.Path
	gitDirs, note := mountedGitDirs(path)
	if len(gitDirs) == 0 {
		return
	}
	_, isTask := tasks[path]
	var findings []string
	for _, dir := range gitDirs {
		findings = append(findings, gitExecFindings(dir)...)
	}
	if note != "" {
		findings = append(findings, note)
	}
	switch {
	case len(findings) > 0 && isTask:
		report.add(checkGitExec, levelWarn, name, "task clone (agent-controlled) has: "+strings.Join(findings, "; "),
			"expected risk in a task clone: never run host git there; use agw task fetch")
	case len(findings) > 0:
		report.add(checkGitExec, levelFail, name, "host git would run programs from: "+strings.Join(findings, "; "),
			"do not run git in "+path+" until reviewed; remove entries you did not add, then rotate host tokens if unsure")
	default:
		report.add(checkGitExec, levelOK, name, "no executable git config or hooks", "")
	}
	switch {
	case isTask:
	case contains(item.Readonly, ".git"):
		report.add(checkGitShared, levelOK, name, ".git is read-only inside the workspace", "")
	default:
		report.add(checkGitShared, levelWarn, name, ".git of "+path+" is writable inside the workspace",
			"recreate the workspace (layout 3 mounts .git read-only), or use `agw task new <name>`")
	}
}

// checkProtectedPaths reports protected paths that exist in the project but
// are writable in the workspace: created after the workspace, or opted out
// with AGENTWISP_WRITABLE. The agent may have written them.
func checkProtectedPaths(report *doctorReport, item Workspace, tasks map[string]taskRecord) {
	if max(item.Layout, 1) < 3 {
		return // workspace.layout already asks for recreation.
	}
	_, isTask := tasks[item.Path]
	var open []string
	for _, name := range protectedProjectPaths {
		if (isTask && name == ".git") || contains(item.Readonly, name) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(item.Path, name)); err == nil {
			open = append(open, name)
		}
	}
	switch {
	case len(open) == 0:
		report.add(checkProtected, levelOK, item.Name, "protected: "+orNone(item.Readonly), "")
	case isTask:
		report.add(checkProtected, levelWarn, item.Name, "writable in the task clone: "+strings.Join(open, ", "),
			"do not open the clone in an IDE or run tools there; review these files in fetched changes")
	default:
		report.add(checkProtected, levelWarn, item.Name, "writable inside the workspace: "+strings.Join(open, ", ")+" (created later or AGENTWISP_WRITABLE)",
			"review them before host tools use them; recreate the workspace to protect them")
	}
}

func orNone(items []string) string {
	if len(items) == 0 {
		return "nothing (no protected paths existed at creation)"
	}
	return strings.Join(items, ", ")
}

// mountedGitDirs returns the git directories reachable from a project: .git
// and nested module repositories. A .git file (worktree) pointing elsewhere is
// reported, because workspace code can rewrite it.
func mountedGitDirs(path string) ([]string, string) {
	gitPath := filepath.Join(path, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return nil, ""
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return []string{gitPath}, ".git is a symlink"
	}
	if info.Mode().IsRegular() {
		data, _ := readLimitedNoFollow(gitPath, 4096)
		target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
		return nil, ".git file points to " + target + " (re-check that directory)"
	}
	dirs := []string{gitPath}
	modules, _ := filepath.Glob(filepath.Join(gitPath, "modules", "*"))
	return append(dirs, modules...), ""
}

// Config keys whose values git executes or which load other config.
var gitExecKeys = map[string]bool{
	"core.fsmonitor": true, "core.pager": true, "core.editor": true, "core.sshcommand": true,
	"core.hookspath": true, "core.gitproxy": true, "core.askpass": true, "core.worktree": true,
	"sequence.editor": true, "diff.external": true, "credential.helper": true, "gpg.program": true,
	"include.path": true, "uploadpack.packobjectshook": true, "web.browser": true,
}

// Subsection keys like filter.<name>.clean.
var gitExecSubsectionKeys = map[string]map[string]bool{
	"filter":     {"clean": true, "smudge": true, "process": true},
	"diff":       {"textconv": true, "command": true},
	"merge":      {"driver": true},
	"credential": {"helper": true},
	"gpg":        {"program": true},
	"includeif":  {"path": true},
	"remote":     {"uploadpack": true, "receivepack": true, "proxy": true},
	"browser":    {"cmd": true, "path": true},
	"man":        {"cmd": true, "path": true},
}

func gitExecFindings(gitDir string) []string {
	var findings []string
	for _, file := range []string{"config", "config.worktree"} {
		for _, key := range gitConfigExecKeys(filepath.Join(gitDir, file)) {
			findings = append(findings, filepath.Base(gitDir)+"/"+file+": "+key)
		}
	}
	hooks, _ := os.ReadDir(filepath.Join(gitDir, "hooks"))
	for _, hook := range hooks {
		if !strings.HasSuffix(hook.Name(), ".sample") {
			findings = append(findings, filepath.Base(gitDir)+"/hooks/"+hook.Name())
		}
	}
	if data, err := readLimitedNoFollow(filepath.Join(gitDir, "info", "attributes"), 64<<10); err == nil && strings.TrimSpace(string(data)) != "" {
		findings = append(findings, filepath.Base(gitDir)+"/info/attributes (may enable filters/diff drivers)")
	}
	return findings
}

// gitConfigExecKeys is a deliberately lenient line parser: it may report a
// key that git would ignore, but should not miss one git would honor.
func gitConfigExecKeys(path string) []string {
	data, err := readLimitedNoFollow(path, 1<<20)
	if err != nil {
		return nil
	}
	var found []string
	section, subsection := "", ""
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			header := line[1:]
			if end := strings.IndexByte(header, ']'); end >= 0 {
				rest := strings.TrimSpace(header[end+1:])
				header = header[:end]
				line = rest // `[core] fsmonitor = x` on one line is valid.
			} else {
				line = ""
			}
			header = strings.TrimSpace(header)
			section, subsection = header, ""
			if name, sub, ok := strings.Cut(header, " "); ok {
				section, subsection = name, strings.Trim(strings.TrimSpace(sub), `"`)
			} else if name, sub, ok := strings.Cut(header, "."); ok {
				section, subsection = name, sub
			}
			section = strings.ToLower(section)
			if line == "" || line[0] == '#' || line[0] == ';' {
				continue
			}
		}
		key, value, _ := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.Trim(strings.TrimSpace(value), `"`)
		full := section + "." + key
		switch {
		case section == "alias" && strings.HasPrefix(value, "!"):
			found = append(found, "alias."+key+" (shell alias)")
		case section == "pager":
			found = append(found, "pager."+key)
		case subsection == "" && gitExecKeys[full]:
			if full == "core.fsmonitor" && (strings.EqualFold(value, "false") || value == "") {
				continue
			}
			found = append(found, full)
		case subsection != "" && gitExecSubsectionKeys[section][key]:
			found = append(found, section+"."+subsection+"."+key)
		}
	}
	return found
}

func checkWorkspaceMonitor(report *doctorReport, name string) {
	cfg, err := loadMonitorConfig(name)
	if err != nil {
		report.add(checkMonitorUp, levelWarn, name, "monitor config unreadable: "+err.Error(), "agw monitor off && agw monitor on")
		return
	}
	if !cfg.Enabled || cfg.Port == 0 {
		return
	}
	healthy := monitorProxyHealthy(cfg)
	if !healthy {
		report.add(checkMonitorUp, levelWarn, name, "monitor enabled but proxy is not responding", "it restarts with the next agw shell/agent; see proxy.log")
	} else {
		report.add(checkMonitorUp, levelOK, name, "proxy healthy", "")
	}
	if host := cfg.ListenHost; healthy && (host == "" || host == "0.0.0.0" || host == "::") && os.Getenv("AGENTWISP_PROXY_LISTEN_HOST") == "" {
		report.add(checkMonitorBind, levelWarn, name, "proxy listens on all interfaces (started by an older agw)",
			"in the project: agw monitor off && agw monitor on")
	}
}
