package agentwisp

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// A task is an independent clone of a host repository with its own workspace.
// The repository's .git is never mounted into the container. Anything inside a
// clone is untrusted after the agent ran, so the host never runs git there:
// results come back as a git bundle streamed out of the container.

var taskNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

type taskRecord struct {
	Name    string `json:"name"`
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	Base    string `json:"base"`
	Created string `json:"created"`
}

// hostGit runs git on the host in a trusted repository only. Hooks and
// fsmonitor are disabled anyway: the main repository may itself have been
// mounted into an `agw add` workspace earlier.
func hostGit(dir string, args ...string) ([]byte, error) {
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-C", dir}
	output, err := commandOutput("git", append(base, args...)...)
	return bytes.TrimSpace(output), err
}

func handleTaskCommand(service *WorkspaceService, args []string, cwd string, in io.Reader, out io.Writer) error {
	usage := fmt.Errorf("usage: agw task new <name> [--from <ref>] | list | fetch <name> | rm <name> [--yes] [--force]")
	if len(args) == 0 {
		return usage
	}
	repo, err := taskRepository(cwd)
	if err != nil {
		return err
	}
	switch args[0] {
	case "new":
		if len(args) != 2 && !(len(args) == 4 && args[2] == "--from") {
			return usage
		}
		from := "HEAD"
		if len(args) == 4 {
			from = args[3]
		}
		return service.NewTask(repo, args[1], from, out)
	case "list", "ls":
		if len(args) != 1 {
			return usage
		}
		return printTasks(repo, out)
	case "fetch":
		if len(args) != 2 {
			return usage
		}
		return service.FetchTask(repo, args[1], out)
	case "rm", "remove":
		if len(args) < 2 {
			return usage
		}
		yes, force := false, false
		for _, arg := range args[2:] {
			switch arg {
			case "--yes", "-y":
				yes = true
			case "--force", "-f":
				force = true
			default:
				return usage
			}
		}
		return service.RemoveTask(repo, args[1], yes, force, in, out)
	default:
		return usage
	}
}

func taskRepository(cwd string) (string, error) {
	top, err := hostGit(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("agw task must run inside the main git repository: %w", err)
	}
	return filepath.EvalSymlinks(string(top))
}

func validateTaskName(name string) error {
	if !taskNamePattern.MatchString(name) {
		return fmt.Errorf("invalid task name %q: use lowercase letters, digits and '-' (max 40)", name)
	}
	return nil
}

func taskBranch(name string) string { return "agw/" + name }

func taskStateDir(repo string) string {
	hash := sha256.Sum256([]byte(filepath.Clean(repo)))
	return filepath.Join(agentWispStateDir(), "tasks", fmt.Sprintf("%x", hash[:8]))
}

func ensureTaskStateDir(repo string) error {
	for _, dir := range []string{agentWispStateDir(), filepath.Join(agentWispStateDir(), "tasks"), taskStateDir(repo)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("task state must be a real directory: %s", dir)
		}
	}
	return nil
}

func loadTask(repo, name string) (taskRecord, error) {
	if err := validateTaskName(name); err != nil {
		return taskRecord{}, err
	}
	data, err := os.ReadFile(filepath.Join(taskStateDir(repo), name+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return taskRecord{}, fmt.Errorf("task %q not found for %s", name, repo)
	}
	if err != nil {
		return taskRecord{}, err
	}
	var task taskRecord
	if err := json.Unmarshal(data, &task); err != nil {
		return taskRecord{}, fmt.Errorf("task %q state is corrupted: %w", name, err)
	}
	if task.Name != name || task.Repo != repo || task.Branch != taskBranch(name) || task.Path != taskClonePath(repo, name) {
		return taskRecord{}, fmt.Errorf("task %q state does not match this repository", name)
	}
	return task, nil
}

func saveTask(task taskRecord) error {
	if err := ensureTaskStateDir(task.Repo); err != nil {
		return err
	}
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(taskStateDir(task.Repo), task.Name+".json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// taskClonePath keeps clones next to the repository so they stay visible to
// the user, e.g. ~/code/api-fix-login for ~/code/api.
func taskClonePath(repo, name string) string {
	return filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-"+name)
}

func (s *WorkspaceService) NewTask(repo, name, from string, out io.Writer) error {
	if err := validateTaskName(name); err != nil {
		return err
	}
	if from == "" || strings.HasPrefix(from, "-") {
		return fmt.Errorf("invalid --from ref %q", from)
	}
	path := taskClonePath(repo, name)
	if strings.ContainsAny(path, ":\r\n") {
		return fmt.Errorf("task path contains unsupported volume delimiter")
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if _, err := os.Stat(filepath.Join(taskStateDir(repo), name+".json")); err == nil {
		return fmt.Errorf("task %q already exists", name)
	}
	base, err := hostGit(repo, "rev-parse", "--verify", "--end-of-options", from+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve %q: %w", from, err)
	}
	// --no-hardlinks: shared object inodes would let the agent alter objects of
	// the main repository. The fresh clone is still trusted until mounted.
	if _, err := hostGit(filepath.Dir(repo), "clone", "--quiet", "--local", "--no-hardlinks", "--no-checkout", "--", repo, path); err != nil {
		return fmt.Errorf("clone task: %w", err)
	}
	if _, err := hostGit(path, "checkout", "--quiet", "-b", taskBranch(name), string(base)); err != nil {
		return fmt.Errorf("checkout task branch: %w", err)
	}
	if _, err := hostGit(path, "remote", "remove", "origin"); err != nil {
		return err
	}
	task := taskRecord{Name: name, Repo: repo, Path: path, Branch: taskBranch(name), Base: string(base), Created: time.Now().UTC().Format(time.RFC3339)}
	if err := saveTask(task); err != nil {
		return err
	}
	container := workspaceContainerName(path)
	if err := s.Ensure(container, path, out); err != nil {
		return fmt.Errorf("task clone created at %s, but workspace failed: %w", path, err)
	}
	fmt.Fprintf(out, "Task %s: %s (branch %s from %.12s)\n", name, path, task.Branch, task.Base)
	fmt.Fprintf(out, "Run the agent: cd %s && agw agent claude\n", shellQuote(path))
	fmt.Fprintf(out, "Bring results back from %s: agw task fetch %s\n", repo, name)
	fmt.Fprintln(out, "Do not run git on the host inside the task directory; the agent controls its .git.")
	return nil
}

func printTasks(repo string, out io.Writer) error {
	entries, err := os.ReadDir(taskStateDir(repo))
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(out, "No tasks for %s\n", repo)
		return nil
	}
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), ".json"); ok && taskNamePattern.MatchString(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Fprintf(out, "No tasks for %s\n", repo)
		return nil
	}
	for _, name := range names {
		task, err := loadTask(repo, name)
		if err != nil {
			fmt.Fprintf(out, "%s\t(error: %s)\n", name, terminalText(err.Error()))
			continue
		}
		fetched := "not fetched"
		if _, err := hostGit(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+task.Branch); err == nil {
			fetched = "fetched"
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", name, task.Branch, fetched, terminalText(task.Path))
	}
	return nil
}

// taskContainer resolves the task workspace and starts it under the lifecycle lock.
func (s *WorkspaceService) taskContainer(task taskRecord, out io.Writer) (string, error) {
	container, err := s.Resolve(task.Path)
	if err != nil {
		return "", err
	}
	if err := s.require(container); err != nil {
		return "", err
	}
	if err := s.startLocked(container, out); err != nil {
		return "", err
	}
	return container, nil
}

func containerGit(args ...string) []string {
	return append([]string{"git", "-c", "safe.directory=/workspace", "-C", "/workspace"}, args...)
}

func (s *WorkspaceService) FetchTask(repo, name string, out io.Writer) error {
	task, err := loadTask(repo, name)
	if err != nil {
		return err
	}
	container, err := s.taskContainer(task, out)
	if err != nil {
		return err
	}
	var head bytes.Buffer
	if err := s.runtime.Exec(container, containerGit("rev-parse", "--verify", "refs/heads/"+task.Branch), &head); err != nil {
		return fmt.Errorf("branch %s not found in the task; commit the work on that branch: %w", task.Branch, err)
	}
	if strings.TrimSpace(head.String()) == task.Base {
		fmt.Fprintf(out, "Nothing to fetch: %s has no commits after %.12s\n", task.Branch, task.Base)
		return nil
	}
	var status bytes.Buffer
	if err := s.runtime.Exec(container, containerGit("status", "--porcelain"), &status); err == nil {
		if changes := strings.Count(status.String(), "\n"); changes > 0 {
			fmt.Fprintf(out, "Warning: %d uncommitted change(s) in the task are not included; commit them to %s first.\n", changes, task.Branch)
		}
	}
	if err := ensureTaskStateDir(repo); err != nil {
		return err
	}
	bundle, err := os.CreateTemp(taskStateDir(repo), ".fetch-*.bundle")
	if err != nil {
		return err
	}
	defer os.Remove(bundle.Name())
	// The base commit exists in the main repository, so exclude it from the bundle.
	exportErr := s.runtime.Exec(container, containerGit("bundle", "create", "--quiet", "-", "refs/heads/"+task.Branch, "^"+task.Base), bundle)
	if closeErr := bundle.Close(); exportErr == nil {
		exportErr = closeErr
	}
	if exportErr != nil {
		return fmt.Errorf("export %s from task (is it committed on that branch?): %w", task.Branch, exportErr)
	}
	ref := "refs/heads/" + task.Branch
	if _, err := hostGit(repo, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", bundle.Name(), ref+":"+ref); err != nil {
		return fmt.Errorf("fetch %s (a rewritten task history is not overwritten; delete the branch in the main repository to replace it): %w", task.Branch, err)
	}
	fetched, _ := hostGit(repo, "rev-parse", "--short", ref)
	fmt.Fprintf(out, "Fetched %s at %s into %s\n", task.Branch, fetched, repo)
	return nil
}

func (s *WorkspaceService) RemoveTask(repo, name string, yes, force bool, in io.Reader, out io.Writer) error {
	task, err := loadTask(repo, name)
	if err != nil {
		return err
	}
	container, resolveErr := s.Resolve(task.Path)
	exists := false
	if resolveErr == nil {
		if exists, err = s.runtime.Exists(container); err != nil {
			return err
		}
	}
	if exists && !force {
		if fetched, err := s.taskFetched(task, container, out); err != nil || !fetched {
			return fmt.Errorf("task %s has work not fetched into %s; run `agw task fetch %s` or use --force", name, repo, name)
		}
	}
	if !yes {
		fmt.Fprintf(out, "Remove task %s? Deletes %s and its workspace container; branch %s in the main repository is kept.\nType the task name to confirm: ", name, task.Path, task.Branch)
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		if strings.TrimSpace(line) != name {
			fmt.Fprintln(out, "Removal cancelled.")
			return nil
		}
	}
	if exists {
		if err := s.Remove(container, true); err != nil {
			return err
		}
	}
	info, err := os.Lstat(task.Path)
	switch {
	case err == nil && info.IsDir():
		if err := os.RemoveAll(task.Path); err != nil {
			return err
		}
	case err == nil:
		return fmt.Errorf("refusing to remove %s: not a directory", task.Path)
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if err := os.Remove(filepath.Join(taskStateDir(repo), name+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintf(out, "Removed task %s\n", name)
	return nil
}

// taskFetched compares the task branch inside the container with the main
// repository. The container's answer is untrusted but only gates a prompt.
func (s *WorkspaceService) taskFetched(task taskRecord, container string, out io.Writer) (bool, error) {
	if err := s.startLocked(container, out); err != nil {
		return false, err
	}
	var head bytes.Buffer
	if err := s.runtime.Exec(container, containerGit("rev-parse", "--verify", "refs/heads/"+task.Branch), &head); err != nil {
		return false, err
	}
	local, err := hostGit(task.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+task.Branch)
	if err != nil {
		return strings.TrimSpace(head.String()) == task.Base, nil
	}
	return strings.TrimSpace(head.String()) == string(local), nil
}

// warnWritableGit is shown when a workspace is created over a git checkout.
func warnWritableGit(path string, out io.Writer) {
	if _, err := os.Lstat(filepath.Join(path, ".git")); err != nil {
		return
	}
	fmt.Fprintln(out, "Warning: this project's .git is writable inside the workspace. Code in the container")
	fmt.Fprintln(out, "can make later host git commands run arbitrary programs (config, hooks). For agent work")
	fmt.Fprintln(out, "prefer `agw task new <name>` from the repository, which never mounts this .git.")
}
