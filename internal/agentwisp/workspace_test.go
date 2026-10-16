package agentwisp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise actual command dispatch without needing Apple container or touching
// real workspaces. The fake only records commands and returns supplied metadata.
func fakeContainer(t *testing.T, items []Workspace) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AGENTWISP_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("AGENTWISP_TEST_DIR", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var all, running strings.Builder
	var records []map[string]any
	for _, item := range items {
		fmt.Fprintln(&all, item.Name)
		if item.Running {
			fmt.Fprintln(&running, item.Name)
		}
		records = append(records, map[string]any{"configuration": map[string]any{
			"id": item.Name, "labels": map[string]string{workspaceLabel: item.Path},
		}})
	}
	data, _ := json.Marshal(records)
	files := map[string]string{
		"all": all.String(), "running": running.String(), "inspect": string(data),
		"container": `#!/bin/sh
printf '%s\n' "$*" >> "$AGENTWISP_TEST_DIR/calls"
case "$1" in
list)
  if [ -n "$AGENTWISP_TEST_LIST_ERROR" ]; then echo 'daemon unavailable' >&2; exit 1; fi
  case "$2" in --all) cat "$AGENTWISP_TEST_DIR/all";; *) cat "$AGENTWISP_TEST_DIR/running";; esac;;
inspect) cat "$AGENTWISP_TEST_DIR/inspect";;
rm) if [ -n "$AGENTWISP_TEST_RM_ERROR" ]; then echo 'remove failed' >&2; exit 1; fi;;
stop) ;;
*) echo 'unexpected command' >&2; exit 1;;
esac
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "calls")
}

func TestWorkspaceIdentityAndLegacyResolution(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a", "api"), filepath.Join(root, "b", "api")
	if workspaceContainerName(a) == workspaceContainerName(b) {
		t.Fatal("same-basename paths collide")
	}
	if !validWorkspaceName(workspaceContainerName(a)) {
		t.Fatal("invalid generated name")
	}
	fakeContainer(t, []Workspace{{Name: "codex-work-api", Path: a}})
	if got, err := defaultWorkspaceService().Resolve(a); err != nil || got != "codex-work-api" {
		t.Fatalf("legacy: %q %v", got, err)
	}
	if got, err := defaultWorkspaceService().Resolve(b); err != nil || got != workspaceContainerName(b) {
		t.Fatalf("collision: %q %v", got, err)
	}
}

func TestResolveRejectsUnknownOwnershipAndDuplicatePath(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprint(duplicate), func(t *testing.T) {
			path := t.TempDir()
			items := []Workspace{{Name: workspaceContainerName(path), Path: "-"}}
			if duplicate {
				items = []Workspace{{Name: "agentwisp-work-a", Path: path}, {Name: "codex-work-a", Path: path}}
			}
			fakeContainer(t, items)
			if _, err := defaultWorkspaceService().Resolve(path); err == nil {
				t.Fatal("ambiguous ownership accepted")
			}
		})
	}
}

func TestListWorkspacesBatchesInspect(t *testing.T) {
	var items []Workspace
	for i := 0; i < 65; i++ {
		items = append(items, Workspace{Name: fmt.Sprintf("agentwisp-work-%d", i), Path: fmt.Sprintf("/code/%d", i), Running: i == 3})
	}
	calls := fakeContainer(t, items)
	got, err := defaultWorkspaceService().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 65 || got[0].Name != "agentwisp-work-3" || got[0].Path != "/code/3" {
		t.Fatalf("incorrect workspace list: %+v", got)
	}
	data, _ := os.ReadFile(calls)
	if len(strings.Split(strings.TrimSpace(string(data)), "\n")) != 4 {
		t.Fatalf("expected 2 list + 2 inspect calls: %s", data)
	}
}

func TestInspectBatchUsesIDsNotOrder(t *testing.T) {
	data := []byte(`[{"id":"agentwisp-work-b","labels":{"dev.agentwisp.workspace":"/b"}}, {"configuration":{"id":"codex-work-a","mounts":[{"source":"/a","target":"/workspace"}]}}]`)
	got, err := workspacePathsFromInspect(data)
	if err != nil || got["agentwisp-work-b"] != "/b" || got["codex-work-a"] != "/a" {
		t.Fatalf("%v %v", got, err)
	}
	for _, input := range []string{`[{}]`, `[`, `[{"id":"x"},{"id":"x"}]`} {
		if _, err := workspacePathsFromInspect([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func TestContainerExistsPropagatesDaemonErrors(t *testing.T) {
	fakeContainer(t, nil)
	t.Setenv("AGENTWISP_TEST_LIST_ERROR", "1")
	if _, err := newAppleRuntime().Exists("agentwisp-work-a"); err == nil {
		t.Fatal("daemon failure treated as missing container")
	}
}

func TestRemoveWorkspace(t *testing.T) {
	for _, running := range []bool{false, true} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("running=%t/force=%t", running, force), func(t *testing.T) {
				project := t.TempDir()
				file := filepath.Join(project, "keep.txt")
				if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				calls := fakeContainer(t, []Workspace{{Name: "agentwisp-work-a", Path: project, Running: running}})
				err := defaultWorkspaceService().Remove("agentwisp-work-a", force)
				if (err != nil) != (running && !force) {
					t.Fatalf("unexpected result %v", err)
				}
				data, _ := os.ReadFile(calls)
				if strings.Contains(string(data), "rm ") == (running && !force) {
					t.Fatalf("wrong deletion commands: %s", data)
				}
				if _, err := os.Stat(file); err != nil {
					t.Fatalf("host file affected: %v", err)
				}
			})
		}
	}
}

func TestRemoveRefusesInvalidNamesAndReportsFailures(t *testing.T) {
	calls := fakeContainer(t, nil)
	for _, name := range []string{"--all", "", "other", "agentwisp-work-a/../../b", "agentwisp-work-"} {
		if err := defaultWorkspaceService().Remove(name, true); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("invalid name invoked container")
	}
	t.Setenv("AGENTWISP_TEST_RM_ERROR", "1")
	if err := defaultWorkspaceService().Remove("agentwisp-work-a", true); err == nil {
		t.Fatal("rm failure ignored")
	}
}

func TestCLIRemoveConfirmation(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"", "yes\n", "agentwisp-work-current\n"} {
		t.Run(fmt.Sprintf("input=%q", input), func(t *testing.T) {
			calls := fakeContainer(t, []Workspace{{Name: "agentwisp-work-current", Path: cwd}})
			var out bytes.Buffer
			if err := Execute([]string{"rm"}, strings.NewReader(input), &out, &out); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(calls)
			if strings.Contains(string(data), "rm ") != (input == "agentwisp-work-current\n") {
				t.Fatalf("unexpected removal: %s", data)
			}
		})
	}
}

func TestWorkspaceImage(t *testing.T) {
	t.Setenv("AGENTWISP_IMAGE", "")
	if got, err := workspaceImage(); err != nil || got != "agentwisp-agents:local" {
		t.Fatalf("default image: %q %v", got, err)
	}
	for _, image := range []string{"--privileged", "bad\nimage", "bad image"} {
		t.Setenv("AGENTWISP_IMAGE", image)
		if _, err := workspaceImage(); err == nil {
			t.Fatalf("accepted %q", image)
		}
	}
	t.Setenv("AGENTWISP_IMAGE", "agentwisp-codex:local")
	if got, err := workspaceImage(); err != nil || got != "agentwisp-codex:local" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestWorkspaceImageOnlyUsedForCreation(t *testing.T) {
	for _, tc := range []struct {
		name            string
		exists, running bool
		image           string
		want            string
	}{
		{name: "new default", want: "agentwisp-agents:local bash"},
		{name: "new override", image: "custom:local", want: "custom:local bash"},
		{name: "existing stopped", exists: true, image: "--invalid-unused", want: "start codex-work-existing"},
		{name: "existing running", exists: true, running: true, want: "exec -it --workdir /workspace codex-work-existing bash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENTWISP_IMAGE", tc.image)
			const name = "codex-work-existing"
			var items []Workspace
			if tc.exists {
				items = []Workspace{{Name: name, Path: "/project", Running: tc.running}}
			}
			calls := fakeContainer(t, items)
			binary := filepath.Join(filepath.Dir(calls), "container")
			script, err := os.ReadFile(binary)
			if err != nil {
				t.Fatal(err)
			}
			script = bytes.Replace(script, []byte("stop) ;;"), []byte("stop|run|start|exec) ;;"), 1)
			if err := os.WriteFile(binary, script, 0o700); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := defaultWorkspaceService().Open(name, "/project", &output, &output); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			log := string(data)
			if !strings.Contains(log, tc.want) {
				t.Fatalf("expected %q in calls: %s", tc.want, log)
			}
			if tc.exists && (strings.Contains(log, "run ") || strings.Contains(log, "rm ")) {
				t.Fatalf("existing container recreated: %s", log)
			}
		})
	}
}
