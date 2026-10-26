package agentwisp

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func gitTestEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(key, "agw test")
	}
	for _, key := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "agw@example.invalid")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// taskFixture creates a main repository and a service whose fake container
// runs git in the task clone, standing in for /workspace inside the container.
func taskFixture(t *testing.T) (*WorkspaceService, *fakeRuntime, string) {
	t.Helper()
	gitTestEnv(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTWISP_STATE_DIR", filepath.Join(root, "state"))
	repo := filepath.Join(root, "api")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "--quiet", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README")
	runGit(t, repo, "commit", "--quiet", "-m", "base")

	s, r, _, _ := testService()
	r.exec = func(command []string, stdout io.Writer) error {
		args := append([]string(nil), command[1:]...)
		for i := range args {
			if args[i] == "/workspace" {
				args[i] = taskClonePath(repo, "fix")
			}
		}
		cmd := exec.Command("git", args...)
		cmd.Stdout = stdout
		return cmd.Run()
	}
	return s, r, repo
}

func TestTaskRoundTripThroughBundle(t *testing.T) {
	s, r, repo := taskFixture(t)
	var out bytes.Buffer
	if err := s.NewTask(repo, "fix", "HEAD", &out); err != nil {
		t.Fatal(err)
	}
	clone := taskClonePath(repo, "fix")
	if clone != filepath.Join(filepath.Dir(repo), "api-fix") {
		t.Fatalf("unexpected clone path %s", clone)
	}
	if got := runGit(t, clone, "branch", "--show-current"); got != "agw/fix" {
		t.Fatalf("clone branch %q", got)
	}
	if remotes := runGit(t, clone, "remote"); remotes != "" {
		t.Fatalf("clone keeps remotes: %q", remotes)
	}
	// No hardlinks: no object file of the clone shares an inode with main.
	assertNoSharedObjects(t, repo, clone)
	if r.launched.Name != "" {
		t.Fatal("task new must not enter a shell")
	}

	if err := s.FetchTask(repo, "fix", &out); err != nil || !strings.Contains(out.String(), "Nothing to fetch") {
		t.Fatalf("empty task: %v %s", err, out.String())
	}

	// The agent commits inside the container.
	if err := os.WriteFile(filepath.Join(clone, "README"), []byte("fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "commit", "--quiet", "-am", "fix")
	// A previous `agw add` of the main repo could have planted hooks there.
	sentinel := filepath.Join(t.TempDir(), "hook-ran")
	hook := "#!/bin/sh\ntouch '" + sentinel + "'\n"
	if err := os.WriteFile(filepath.Join(repo, ".git", "hooks", "reference-transaction"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := s.FetchTask(repo, "fix", &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if runGit(t, repo, "rev-parse", "agw/fix") != runGit(t, clone, "rev-parse", "HEAD") {
		t.Fatal("branch not fetched")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("host git ran a repository hook")
	}
	if runGit(t, repo, "show", "agw/fix:README") != "fixed" {
		t.Fatal("wrong content fetched")
	}

	// A rewritten task history is reported, not silently forced over.
	runGit(t, clone, "commit", "--quiet", "--amend", "-m", "rewritten")
	if err := s.FetchTask(repo, "fix", &out); err == nil {
		t.Fatal("non-fast-forward fetch accepted")
	}
}

func assertNoSharedObjects(t *testing.T, repo, clone string) {
	t.Helper()
	inodes := map[uint64]bool{}
	walk := func(root string, check bool) {
		_ = filepath.Walk(filepath.Join(root, ".git", "objects"), func(path string, info os.FileInfo, err error) error {
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
			ino := fileInode(info)
			if check && inodes[ino] {
				t.Fatalf("shared object inode: %s", path)
			}
			inodes[ino] = true
			return nil
		})
	}
	walk(repo, false)
	walk(clone, true)
}

func TestTaskRemoveRequiresFetchedWork(t *testing.T) {
	s, _, repo := taskFixture(t)
	var out bytes.Buffer
	if err := s.NewTask(repo, "fix", "HEAD", &out); err != nil {
		t.Fatal(err)
	}
	clone := taskClonePath(repo, "fix")
	runGit(t, clone, "commit", "--quiet", "--allow-empty", "-m", "work")

	if err := s.RemoveTask(repo, "fix", true, false, strings.NewReader(""), &out); err == nil {
		t.Fatal("removed unfetched work")
	}
	if err := s.RemoveTask(repo, "fix", false, false, strings.NewReader("wrong\n"), &out); err == nil {
		t.Fatal("unfetched check skipped before prompt")
	}
	if err := s.FetchTask(repo, "fix", &out); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveTask(repo, "fix", false, false, strings.NewReader("wrong\n"), &out); err != nil || !strings.Contains(out.String(), "cancelled") {
		t.Fatalf("prompt not honored: %v", err)
	}
	if err := s.RemoveTask(repo, "fix", false, false, strings.NewReader("fix\n"), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(clone); !os.IsNotExist(err) {
		t.Fatal("clone not removed")
	}
	if runGit(t, repo, "branch", "--list", "agw/fix") == "" {
		t.Fatal("fetched branch must be kept in the main repository")
	}
}

func TestTaskRejectsUnsafeInput(t *testing.T) {
	s, _, repo := taskFixture(t)
	for _, name := range []string{"", "../x", "Fix", "-x", strings.Repeat("a", 41), "a/b"} {
		if err := s.NewTask(repo, name, "HEAD", io.Discard); err == nil {
			t.Fatalf("accepted task name %q", name)
		}
	}
	if err := s.NewTask(repo, "ok", "--upload-pack=touch", io.Discard); err == nil {
		t.Fatal("accepted option as ref")
	}
	if _, err := loadTask(repo, "missing"); err == nil {
		t.Fatal("loaded missing task")
	}
}

func TestTaskAgentRunsFromTheRepositoryAndKeepsCloneGitWritable(t *testing.T) {
	s, r, repo := taskFixture(t)
	r.exists = false
	var created RuntimeCreate
	r.create = func(req RuntimeCreate) { created = req; r.exists = true }
	var out bytes.Buffer
	if err := s.NewTask(repo, "fix", "HEAD", &out); err != nil {
		t.Fatal(err)
	}
	clone := taskClonePath(repo, "fix")
	// The agent commits in the clone; agw task fetch reads it as data.
	for _, mount := range created.Mounts {
		if mount.ReadOnly && strings.HasSuffix(mount.Host, ".git") {
			t.Fatalf("task .git mounted read-only: %+v", mount)
		}
	}
	if strings.Contains(out.String(), "&& agw agent") || !strings.Contains(out.String(), "agw task agent fix") {
		t.Fatalf("hint %q", out.String())
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"agent", "fix"}, "claude"},
		{[]string{"agent", "fix", "codex", "--", "--model", "m"}, "codex"},
	} {
		if err := handleTaskCommand(s, tc.args, repo, nil, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
		if r.launched.Name != workspaceContainerName(clone) || !slices.Contains(r.launched.Command, tc.want) {
			t.Fatalf("%v launched %+v", tc.args, r.launched)
		}
	}
	if err := handleTaskCommand(s, []string{"agent", "nope"}, repo, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("unknown task accepted")
	}
}
