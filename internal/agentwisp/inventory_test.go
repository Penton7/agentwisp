package agentwisp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestJSONInventoryContract(t *testing.T) {
	s, r, _, events := testService()
	r.items = []Workspace{
		{ID: "ws_first", Name: "agentwisp-work-first", Path: "/project/\"quoted\"/项目\n\x1b", Running: true},
		{ID: "ws_legacy", Name: "codex-work-legacy", Path: "-"},
	}
	for _, alias := range []string{"list", "ls", "."} {
		*events = nil
		var out, stderr bytes.Buffer
		if err := executeWithService(s, []string{alias, "--json"}, strings.NewReader(""), &out, &stderr); err != nil {
			t.Fatal(err)
		}
		var actual map[string]any
		decoder := json.NewDecoder(&out)
		if err := decoder.Decode(&actual); err != nil {
			t.Fatal(err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			t.Fatalf("unexpected output after JSON: %v", err)
		}
		want := map[string]any{"schema_version": float64(1), "workspaces": []any{
			map[string]any{"id": "ws_first", "container_name": "agentwisp-work-first", "host_path": r.items[0].Path, "container_state": "running"},
			map[string]any{"id": "ws_legacy", "container_name": "codex-work-legacy", "host_path": nil, "container_state": "stopped"},
		}}
		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("schema changed: %#v", actual)
		}
		if stderr.Len() != 0 || !reflect.DeepEqual(*events, []string{"list"}) {
			t.Fatalf("unexpected side effects: %v %s", *events, stderr.String())
		}
	}
}
func TestEmptyJSONInventoryIsArray(t *testing.T) {
	s, _, _, _ := testService()
	var out bytes.Buffer
	if err := writeWorkspaceListJSON(s, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\"schema_version\":1,\"workspaces\":[]}\n" {
		t.Fatal(out.String())
	}
}
func TestJSONInspectExactIdentity(t *testing.T) {
	s, r, _, events := testService()
	r.items = []Workspace{{ID: "ws_first", Name: "codex-work-legacy", Path: "/project", Running: true}}
	for _, args := range [][]string{{"inspect", "ws_first", "--json"}, {"inspect", "--json", "ws_first"}} {
		*events = nil
		var out bytes.Buffer
		if err := executeWithService(s, args, strings.NewReader(""), &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		want := "{\"schema_version\":1,\"workspace\":{\"id\":\"ws_first\",\"container_name\":\"codex-work-legacy\",\"host_path\":\"/project\",\"container_state\":\"running\"}}\n"
		if out.String() != want || !reflect.DeepEqual(*events, []string{"list"}) {
			t.Fatalf("%s %v", out.String(), *events)
		}
	}
	for _, id := range []string{"ws_missing", "ws_f", "codex-work-legacy", "$(touch forbidden)"} {
		var out bytes.Buffer
		err := executeWithService(s, []string{"inspect", id, "--json"}, strings.NewReader(""), &out, io.Discard)
		if !errors.Is(err, ErrWorkspaceAbsent) || out.Len() != 0 {
			t.Fatalf("%s: %v %q", id, err, out.String())
		}
	}
	r.items = append(r.items, r.items[0])
	var out bytes.Buffer
	if err := writeWorkspaceInspectJSON(s, "ws_first", &out); err == nil || out.Len() != 0 {
		t.Fatalf("ambiguous ID accepted: %v", err)
	}
}
func TestJSONErrorsProduceNoStdout(t *testing.T) {
	for _, failure := range []error{ErrRuntimeUnavailable, ErrRuntimePermission, ErrRuntimeFailed} {
		for _, args := range [][]string{{"list", "--json"}, {"inspect", "ws_test", "--json"}} {
			s, r, _, events := testService()
			r.failOp = "list"
			r.failure = failure
			var out bytes.Buffer
			err := executeWithService(s, args, strings.NewReader(""), &out, io.Discard)
			if !errors.Is(err, failure) || out.Len() != 0 || !reflect.DeepEqual(*events, []string{"list"}) {
				t.Fatalf("%v %v %q", args, err, out.String())
			}
		}
	}
}
func TestJSONRejectsInvalidArgumentsBeforeRuntime(t *testing.T) {
	for _, args := range [][]string{
		{"list", "--json", "extra"}, {"ls", "--json", "--json"}, {"list", "--force"},
		{"inspect"}, {"inspect", "ws_test"}, {"inspect", "--json"}, {"inspect", "", "--json"},
		{"inspect", "ws_test", "--json", "extra"}, {"inspect", "ws_test", "--json", "--json"}, {"inspect", "--force", "ws_test", "--json"},
	} {
		s, _, _, events := testService()
		var out bytes.Buffer
		if err := executeWithService(s, args, strings.NewReader(""), &out, io.Discard); err == nil || out.Len() != 0 || len(*events) != 0 {
			t.Fatalf("%v: %v %q %v", args, err, out.String(), *events)
		}
	}
}

type failingInventoryWriter struct{}

func (failingInventoryWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestJSONPropagatesWriteFailure(t *testing.T) {
	s, r, _, _ := testService()
	r.items = []Workspace{{ID: "ws_test"}}
	if err := writeWorkspaceListJSON(s, failingInventoryWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if err := writeWorkspaceInspectJSON(s, "ws_test", failingInventoryWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}
func TestJSONDoesNotExposeRawInspectOrMonitorState(t *testing.T) {
	name := "codex-work-legacy"
	calls := fakeContainer(t, []Workspace{{Name: name, Path: "/project"}})
	// Unrelated environment, labels and metadata must never be passed through.
	raw := `[{"id":"codex-work-legacy","labels":{"dev.codexbox.workspace":"/project","private":"SECRET_LABEL"},"environment":{"TOKEN":"SECRET_ENV"},"auth":"SECRET_AUTH"}]`
	if err := os.WriteFile(filepath.Join(filepath.Dir(calls), "inspect"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	// Invalid monitor state would break a launch; inventory must not read it.
	t.Setenv("AGENTWISP_STATE_DIR", filepath.Join(filepath.Dir(calls), "not-a-directory"))
	if err := os.WriteFile(os.Getenv("AGENTWISP_STATE_DIR"), []byte("SECRET_MONITOR"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"list", "--json"}, {"inspect", string(discoveredWorkspaceID(name)), "--json"}} {
		var out bytes.Buffer
		if err := Execute(args, strings.NewReader(""), &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(out.Bytes()) || strings.Contains(out.String(), "SECRET_") {
			t.Fatalf("unexpected output: %q", out.String())
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !strings.HasPrefix(line, "list ") && !strings.HasPrefix(line, "inspect ") {
			t.Fatalf("mutation: %s", line)
		}
	}
}
