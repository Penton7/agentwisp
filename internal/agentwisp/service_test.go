package agentwisp

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeRuntime struct {
	items           []Workspace
	exists, running bool
	failOp          string
	failure         error
	events          *[]string
	launched        RuntimeLaunch
}

func (f *fakeRuntime) record(op string) error {
	*f.events = append(*f.events, op)
	if op == f.failOp {
		return f.failure
	}
	return nil
}
func (f *fakeRuntime) List() ([]Workspace, error)   { return f.items, f.record("list") }
func (f *fakeRuntime) Exists(string) (bool, error)  { return f.exists, f.record("exists") }
func (f *fakeRuntime) Running(string) (bool, error) { return f.running, f.record("running") }
func (f *fakeRuntime) Start(string) error           { return f.record("start") }
func (f *fakeRuntime) Stop(string) error            { return f.record("stop") }
func (f *fakeRuntime) Remove(_ string, force bool) error {
	if force {
		return f.record("remove-force")
	}
	return f.record("remove")
}
func (f *fakeRuntime) Create(RuntimeCreate) error         { return f.record("create") }
func (f *fakeRuntime) Addresses(string) ([]net.IP, error) { return nil, f.record("addresses") }
func (f *fakeRuntime) CheckAgent(string, string) error    { return f.record("check-agent") }
func (f *fakeRuntime) Launch(req RuntimeLaunch, _ io.Writer) error {
	f.launched = req
	return f.record("launch")
}

type fakeMonitor struct {
	events  *[]string
	failure error
}

func (m fakeMonitor) Disable(string) error {
	*m.events = append(*m.events, "disable")
	return m.failure
}
func (m fakeMonitor) Prepare(req RuntimeLaunch) (RuntimeLaunch, func(), error) {
	*m.events = append(*m.events, "prepare")
	return req, func() { *m.events = append(*m.events, "cleanup") }, m.failure
}
func testService() (*WorkspaceService, *fakeRuntime, *fakeMonitor, *[]string) {
	events := []string{}
	r := &fakeRuntime{events: &events, exists: true}
	m := &fakeMonitor{events: &events}
	return NewWorkspaceService(r, m), r, m, &events
}

func TestServiceDoesNotCreateOnRuntimeFailure(t *testing.T) {
	for _, failure := range []error{ErrRuntimeUnavailable, ErrRuntimePermission, ErrRuntimeFailed} {
		s, r, _, events := testService()
		r.exists = false
		r.failOp = "exists"
		r.failure = failure
		err := s.Open("agentwisp-work-test", "/project", io.Discard, io.Discard)
		if !errors.Is(err, failure) || !reflect.DeepEqual(*events, []string{"exists"}) {
			t.Fatalf("failure caused mutation: %v %v", err, *events)
		}
	}
}
func TestServiceLifecycleFailureBoundaries(t *testing.T) {
	failure := errors.New("test failure")
	for _, tc := range []struct {
		name, failOp   string
		monitorFailure bool
		action         string
		want           []string
	}{
		{name: "stop", action: "stop", want: []string{"exists", "stop", "disable"}},
		{name: "stop fails", action: "stop", failOp: "stop", want: []string{"exists", "stop"}},
		{name: "proxy fails after stop", action: "stop", monitorFailure: true, want: []string{"exists", "stop", "disable"}},
		{name: "remove", action: "remove", want: []string{"exists", "running", "disable", "remove-force"}},
		{name: "proxy prevents remove", action: "remove", monitorFailure: true, want: []string{"exists", "running", "disable"}},
		{name: "remove fails", action: "remove", failOp: "remove-force", want: []string{"exists", "running", "disable", "remove-force"}},
		{name: "enter", action: "enter", want: []string{"exists", "running", "start", "prepare", "launch", "cleanup"}},
		{name: "start prevents launch", action: "enter", failOp: "start", want: []string{"exists", "running", "start"}},
		{name: "launch cleans up", action: "enter", failOp: "launch", want: []string{"exists", "running", "start", "prepare", "launch", "cleanup"}},
		{name: "prepare prevents launch", action: "enter", monitorFailure: true, want: []string{"exists", "running", "start", "prepare", "cleanup"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, m, events := testService()
			r.failOp = tc.failOp
			r.failure = failure
			if tc.monitorFailure {
				m.failure = failure
			}
			var err error
			switch tc.action {
			case "stop":
				err = s.Stop("agentwisp-work-test")
			case "remove":
				err = s.Remove("agentwisp-work-test", true)
			case "enter":
				err = s.Enter("agentwisp-work-test", io.Discard, io.Discard)
			}
			wantFailure := tc.monitorFailure || tc.failOp != ""
			if errors.Is(err, failure) != wantFailure || !reflect.DeepEqual(*events, tc.want) {
				t.Fatalf("err=%v, events=%v; want %v", err, *events, tc.want)
			}
		})
	}
}
func TestServiceAbsentDoesNotMutate(t *testing.T) {
	for _, action := range []string{"enter", "shell", "stop", "remove", "agent"} {
		s, r, _, events := testService()
		r.exists = false
		var err error
		switch action {
		case "enter":
			err = s.Enter("agentwisp-work-test", io.Discard, io.Discard)
		case "shell":
			err = s.Shell("agentwisp-work-test", io.Discard)
		case "stop":
			err = s.Stop("agentwisp-work-test")
		case "remove":
			err = s.Remove("agentwisp-work-test", true)
		case "agent":
			err = s.RunAgent("agentwisp-work-test", []string{"claude"}, io.Discard, io.Discard)
		}
		if !errors.Is(err, ErrWorkspaceAbsent) || !reflect.DeepEqual(*events, []string{"exists"}) {
			t.Fatalf("%s: %v %v", action, err, *events)
		}
	}
}
func TestCLIAndManagerUseInjectedService(t *testing.T) {
	s, r, _, events := testService()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	r.items = []Workspace{{Name: "codex-work-legacy", Path: cwd}}
	var out bytes.Buffer
	if err := executeWithService(s, []string{"stop"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*events, []string{"list", "exists", "stop", "disable"}) {
		t.Fatal(*events)
	}
	*events = nil
	if err := chooseWorkspace(s, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*events, []string{"list"}) || !strings.Contains(out.String(), "codex-work-legacy") {
		t.Fatalf("%v %s", *events, out.String())
	}
}
func TestServiceRefreshesRunningStateOnEnter(t *testing.T) {
	s, r, _, events := testService()
	r.running = true
	if err := s.Enter("agentwisp-work-test", io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*events, []string{"exists", "running", "prepare", "launch", "cleanup"}) {
		t.Fatal(*events)
	}
}
