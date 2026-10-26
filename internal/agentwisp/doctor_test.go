package agentwisp

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestDoctorRegistryLayoutsAndDocsInSync(t *testing.T) {
	docs, err := os.ReadFile(filepath.Join("..", "..", "docs", "doctor.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range doctorChecks {
		if !strings.Contains(string(docs), "`"+id+"`") {
			t.Errorf("check %s is not documented in docs/doctor.md", id)
		}
	}
	// Every check constant in doctor.go must be registered.
	source, _ := os.ReadFile("doctor.go")
	for _, match := range regexp.MustCompile(`check\w+\s+= "([a-z.-]+)"`).FindAllStringSubmatch(string(source), -1) {
		if !slices.Contains(doctorChecks, match[1]) {
			t.Errorf("check %s missing from doctorChecks", match[1])
		}
	}
	for v := 1; v <= currentWorkspaceLayout; v++ {
		if workspaceLayoutHistory[v] == "" {
			t.Errorf("layout %d has no history entry", v)
		}
		if !regexp.MustCompile(`(?m)^\| ` + regexp.QuoteMeta(string(rune('0'+v))) + ` \|`).Match(docs) {
			t.Errorf("layout %d is not documented", v)
		}
	}
	if len(workspaceLayoutHistory) != currentWorkspaceLayout {
		t.Error("layout history has entries beyond the current layout")
	}
}

func TestGitConfigExecKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	config := `[core]
	repositoryformatversion = 0
	fsmonitor = false
[core] pager = less
[CORE]
	FSMonitor = "touch /tmp/x"
	hooksPath = .githooks
[user]
	name = someone
[remote "origin"]
	url = git@example.com:x.git
	uploadpack = evil
[filter "lfs"]
	clean = git-lfs clean -- %f
[alias]
	co = checkout
	pwn = "!sh -c id"
[includeIf "gitdir:/x/"]
	path = /tmp/other
# fsmonitor = commented
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	got := gitConfigExecKeys(path)
	want := []string{"core.pager", "core.fsmonitor", "core.hookspath", "remote.origin.uploadpack", "filter.lfs.clean", "alias.pwn (shell alias)", "includeif.gitdir:/x/.path"}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestInspectReadsLayoutLabel(t *testing.T) {
	data := []byte(`[{"configuration":{"id":"agentwisp-work-new","labels":{"dev.agentwisp.workspace":"/a","dev.agentwisp.layout":"2"}}},{"configuration":{"id":"codex-work-old","labels":{"dev.codexbox.workspace":"/b"}}}]`)
	items, err := inspectedWorkspaces(data)
	if err != nil || items["agentwisp-work-new"].layout != 2 || items["codex-work-old"].layout != 0 {
		t.Fatalf("%+v %v", items, err)
	}
}

func TestDoctorReportsOldLayoutsAndPoisonedGit(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTWISP_STATE_DIR", filepath.Join(root, "state"))
	project := filepath.Join(root, "project")
	clean := filepath.Join(root, "clean")
	clone := filepath.Join(root, "repo-fix")
	for _, dir := range []string{project, clean, clone} {
		if err := os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-commit.sample"), []byte("#"), 0o600)
		_ = os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]\n\tbare = false\n"), 0o600)
	}
	_ = os.WriteFile(filepath.Join(project, ".git", "config"), []byte("[core]\n\tfsmonitor = /tmp/evil\n"), 0o600)
	_ = os.WriteFile(filepath.Join(clone, ".git", "hooks", "post-checkout"), []byte("#!/bin/sh"), 0o700)
	task := taskRecord{Name: "fix", Repo: filepath.Join(root, "repo"), Path: clone, Branch: "agw/fix", Base: "abc"}
	if err := saveTask(task); err != nil {
		t.Fatal(err)
	}

	s, r, _, _ := testService()
	r.items = []Workspace{
		{Name: "agentwisp-work-project", Path: project},
		{Name: "agentwisp-work-clean", Path: clean, Layout: currentWorkspaceLayout},
		{Name: "agentwisp-work-clone", Path: clone, Layout: currentWorkspaceLayout},
		{Name: "codex-work-gone", Path: filepath.Join(root, "missing")},
	}
	report := s.Doctor()
	find := func(check, subject string) doctorFinding {
		for _, f := range report.Findings {
			if f.Check == check && f.Subject == subject {
				return f
			}
		}
		t.Fatalf("no %s finding for %s: %+v", check, subject, report.Findings)
		return doctorFinding{}
	}
	if f := find(checkGitExec, "agentwisp-work-project"); f.Level != levelFail || !strings.Contains(f.Message, "core.fsmonitor") {
		t.Fatalf("%+v", f)
	}
	if f := find(checkLayout, "agentwisp-work-project"); f.Level != levelWarn || !strings.Contains(f.Message, "keep-alive") {
		t.Fatalf("%+v", f)
	}
	if f := find(checkGitExec, "agentwisp-work-clean"); f.Level != levelOK {
		t.Fatalf("%+v", f)
	}
	if f := find(checkGitShared, "agentwisp-work-clean"); f.Level != levelWarn {
		t.Fatalf("%+v", f)
	}
	if f := find(checkGitExec, "agentwisp-work-clone"); f.Level != levelWarn || !strings.Contains(f.Message, "post-checkout") {
		t.Fatalf("task clone: %+v", f)
	}
	for _, f := range report.Findings {
		if f.Subject == "agentwisp-work-clone" && f.Check == checkGitShared {
			t.Fatal("task clone reported as shared project .git")
		}
	}
	if f := find(checkPath, "codex-work-gone"); f.Level != levelWarn {
		t.Fatalf("%+v", f)
	}
	if f := find(checkLayout, "agentwisp-work-clean"); f.Level != levelOK {
		t.Fatalf("%+v", f)
	}

	var out bytes.Buffer
	if err := handleDoctorCommand(s, []string{"--json"}, &out); !errors.Is(err, errDoctorProblems) {
		t.Fatalf("fail finding must fail the command: %v", err)
	}
	var decoded doctorReport
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || decoded.SchemaVersion != 1 || len(decoded.Findings) != len(report.Findings) {
		t.Fatalf("json: %v %s", err, out.String())
	}
}

func TestDoctorStrictAndRuntimeFailure(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTWISP_STATE_DIR", state)
	s, r, _, _ := testService()
	r.items = []Workspace{{Name: "agentwisp-work-x", Path: "-", Layout: 1}}
	var out bytes.Buffer
	if err := handleDoctorCommand(s, nil, &out); err != nil {
		t.Fatalf("warnings alone must not fail: %v\n%s", err, out.String())
	}
	if err := handleDoctorCommand(s, []string{"--strict"}, &out); !errors.Is(err, errDoctorProblems) {
		t.Fatalf("strict: %v", err)
	}
	r.failOp, r.failure = "list", ErrRuntimeUnavailable
	if f := s.Doctor().Findings; f[len(f)-1].Check != checkRuntime || f[len(f)-1].Level != levelFail {
		t.Fatalf("%+v", f)
	}
}

func TestDoctorFlagsLooseStatePermissions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTWISP_STATE_DIR", root)
	if err := saveMonitorConfig("agentwisp-work-x", monitorConfig{Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	report := doctorReport{}
	checkHostState(&report)
	if report.Findings[0].Level != levelOK {
		t.Fatalf("%+v", report.Findings)
	}
	if err := os.Chmod(monitorConfigPath("agentwisp-work-x"), 0o644); err != nil {
		t.Fatal(err)
	}
	report = doctorReport{}
	checkHostState(&report)
	if report.Findings[0].Level != levelFail {
		t.Fatalf("world-readable token not reported: %+v", report.Findings)
	}
}
