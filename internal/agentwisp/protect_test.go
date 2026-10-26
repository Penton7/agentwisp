package agentwisp

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func protectedProject(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	for _, dir := range []string{".git/hooks", ".idea", "scripts/hooks"} {
		if err := os.MkdirAll(filepath.Join(project, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		".git/config":             "[core]\n\thooksPath = scripts/hooks\n[core]\n\thooksPath = ../outside\n",
		".pre-commit-config.yaml": "repos: []\n",
		"main.go":                 "package main\n",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(project, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(project, ".vscode")); err != nil {
		t.Fatal(err)
	}
	return project
}

func noEnv(string) string { return "" }

func TestWorkspaceProtectionMountsExistingDangerousPathsReadOnly(t *testing.T) {
	project := protectedProject(t)
	mounts, protected, notes, err := workspaceProtection(project, false, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	// Absent paths are not mounted; the symlink is reported, not followed;
	// a hooksPath inside the project is protected, one outside is not.
	want := []string{".git", ".idea", ".pre-commit-config.yaml", "scripts/hooks"}
	if !reflect.DeepEqual(protected, want) {
		t.Fatalf("protected %q", protected)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], ".vscode") {
		t.Fatalf("notes %q", notes)
	}
	for i, mount := range mounts {
		if !mount.ReadOnly || mount.Host != filepath.Join(project, want[i]) || mount.Container != "/workspace/"+want[i] {
			t.Fatalf("mount %+v", mount)
		}
	}

	// A task clone keeps .git writable (the agent commits there) and does not
	// follow the clone's hooksPath, but still protects IDE configuration.
	_, protected, _, err = workspaceProtection(project, true, noEnv)
	if err != nil || !reflect.DeepEqual(protected, []string{".idea", ".pre-commit-config.yaml"}) {
		t.Fatalf("task protected %q %v", protected, err)
	}

	env := func(value string) func(string) string {
		return func(key string) string {
			if key == "AGENTWISP_WRITABLE" {
				return value
			}
			return ""
		}
	}
	_, protected, _, err = workspaceProtection(project, false, env(" .git , .idea"))
	if err != nil || !reflect.DeepEqual(protected, []string{".pre-commit-config.yaml"}) {
		t.Fatalf("opt-out protected %q %v", protected, err)
	}
	for _, bad := range []string{"main.go", "../x", ".git/config"} {
		if _, _, _, err := workspaceProtection(project, false, env(bad)); err == nil {
			t.Fatalf("AGENTWISP_WRITABLE=%s accepted", bad)
		}
	}
}

func TestWorkTreeHooksStaysInsideProject(t *testing.T) {
	project := t.TempDir()
	os.MkdirAll(filepath.Join(project, ".git"), 0o755)
	config := "[core]\nhooksPath=\"tools/hooks/\"\n[core]\nhooksPath=/abs\n[core]\nhookspath = ../../x\n[core]\nhooksPath=.husky\n[other]\nhooksPath=nope\n[]\n"
	os.WriteFile(filepath.Join(project, ".git", "config"), []byte(config), 0o644)
	if got := workTreeHooks(project); !reflect.DeepEqual(got, []string{"tools/hooks"}) {
		t.Fatalf("hooks %q", got)
	}
}

func TestNewWorkspaceVerifiesProtectionOrRemovesIt(t *testing.T) {
	t.Setenv("AGENTWISP_STATE_DIR", t.TempDir())
	t.Setenv("AGENTWISP_WRITABLE", "")
	project := protectedProject(t)
	for _, tc := range []struct {
		name    string
		exec    func([]string, io.Writer) error
		wantErr string
		events  []string
	}{
		{"read-only", func([]string, io.Writer) error { return nil }, "", []string{"exists", "create", "exec"}},
		{"writable", func(_ []string, out io.Writer) error { fmt.Fprintln(out, ".git"); return nil }, "still writable: .git", []string{"exists", "create", "exec", "remove-force"}},
		{"exec-fails", func([]string, io.Writer) error { return errors.New("no sh") }, "check failed", []string{"exists", "create", "exec", "remove-force"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, _, events := testService()
			r.exists = false
			var created RuntimeCreate
			var checked []string
			r.create = func(req RuntimeCreate) { created = req }
			r.exec = func(command []string, out io.Writer) error {
				checked = command[4:]
				return tc.exec(command, out)
			}
			var out strings.Builder
			err := s.Open("agentwisp-work-test", project, &out, io.Discard)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err %v", err)
			}
			if !reflect.DeepEqual(created.Protected, checked) || !slices.Contains(created.Protected, ".git") {
				t.Fatalf("created %q, checked %q", created.Protected, checked)
			}
			if got := (*events)[:len(tc.events)]; !reflect.DeepEqual(got, tc.events) {
				t.Fatalf("events %q", *events)
			}
			if tc.wantErr == "" && !strings.Contains(out.String(), "cannot commit there") {
				t.Fatalf("output %q", out.String())
			}
		})
	}
}

func TestFileMountFailureFallsBackToDirectories(t *testing.T) {
	t.Setenv("AGENTWISP_STATE_DIR", t.TempDir())
	t.Setenv("AGENTWISP_WRITABLE", "")
	project := protectedProject(t)
	s, r, _, events := testService()
	r.exists = false
	var attempts []RuntimeCreate
	r.create = func(req RuntimeCreate) {
		attempts = append(attempts, req)
		if len(attempts) == 2 {
			r.failOp = "" // Only the first create fails.
		}
	}
	r.failOp, r.failure = "create", errors.New("file mounts unsupported")
	var out strings.Builder
	if err := s.Ensure("agentwisp-work-test", project, &out); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || !reflect.DeepEqual(attempts[1].Protected, []string{".git", ".idea", "scripts/hooks"}) {
		t.Fatalf("attempts %+v", attempts)
	}
	for _, mount := range attempts[1].Mounts {
		if strings.HasSuffix(mount.Host, ".pre-commit-config.yaml") {
			t.Fatal("file mount retried")
		}
	}
	if !strings.Contains(out.String(), "writable: .pre-commit-config.yaml") || !slices.Contains(*events, "remove-force") {
		t.Fatalf("output %q events %q", out.String(), *events)
	}
}

func TestReadonlyLabelKeepsOnlyKnownPaths(t *testing.T) {
	record := []byte(`{"configuration":{"id":"x","labels":{"dev.agentwisp.readonly":".git,.idea,../../etc,.git,scripts/hooks"}}}`)
	if got := workspaceReadonlyFromInspect(record); !reflect.DeepEqual(got, []string{".git", ".idea"}) {
		t.Fatalf("readonly %q", got)
	}
}

func TestDoctorReportsUnprotectedPaths(t *testing.T) {
	project := protectedProject(t)
	var report doctorReport
	item := Workspace{Name: "agentwisp-work-p", Path: project, Layout: currentWorkspaceLayout, Readonly: []string{".git", ".idea"}}
	checkProtectedPaths(&report, item, nil)
	checkMountedGit(&report, item, nil)
	var protected, shared doctorFinding
	for _, f := range report.Findings {
		switch f.Check {
		case checkProtected:
			protected = f
		case checkGitShared:
			shared = f
		}
	}
	// .vscode (symlink) and .pre-commit-config.yaml exist but are writable.
	if protected.Level != levelWarn || !strings.Contains(protected.Message, ".vscode, .pre-commit-config.yaml") {
		t.Fatalf("protected %+v", protected)
	}
	if shared.Level != levelOK {
		t.Fatalf("git-shared %+v", shared)
	}
	report = doctorReport{}
	item.Readonly = nil
	checkMountedGit(&report, item, nil)
	for _, f := range report.Findings {
		if f.Check == checkGitShared && f.Level != levelWarn {
			t.Fatalf("writable .git not reported: %+v", f)
		}
	}
}
