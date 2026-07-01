package agentwisp

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestWorkspacePathFromInspectLabel(t *testing.T) {
	data := []byte(`[
		{
			"configuration": {
				"labels": {
					"dev.agentwisp.workspace": "/Users/me/project"
				}
			}
		}
	]`)

	got, err := workspacePathFromInspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/me/project" {
		t.Fatalf("got %q", got)
	}
}

func TestWorkspacePathFromLegacyInspectLabel(t *testing.T) {
	data := []byte(`{
		"labels": {
			"dev.codexbox.workspace": "/Users/me/legacy-project"
		}
	}`)

	got, err := workspacePathFromInspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/me/legacy-project" {
		t.Fatalf("got %q", got)
	}
}

func TestAgentWispIdentifiers(t *testing.T) {
	if containerPrefix != "agentwisp-work-" {
		t.Fatalf("container prefix: %q", containerPrefix)
	}
	if workspaceLabel != "dev.agentwisp.workspace" {
		t.Fatalf("workspace label: %q", workspaceLabel)
	}
	if managedLabel != "dev.agentwisp.managed" {
		t.Fatalf("managed label: %q", managedLabel)
	}
}

func TestWorkspacePathFromInspectMount(t *testing.T) {
	data := []byte(`{
		"mounts": [
			{
				"type": "bind",
				"source": "/Users/me/legacy-project",
				"destination": "/workspace"
			}
		]
	}`)

	got, err := workspacePathFromInspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/me/legacy-project" {
		t.Fatalf("got %q", got)
	}
}

func TestWorkspacePathFromInspectVolumeString(t *testing.T) {
	data := []byte(`{"volumes":["/Users/me/project:/workspace"]}`)

	got, err := workspacePathFromInspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/me/project" {
		t.Fatalf("got %q", got)
	}
}

func TestPrintWorkspaces(t *testing.T) {
	var out bytes.Buffer
	printWorkspaces(&out, []workspace{
		{Name: "agentwisp-work-api", Path: "/code/api", Running: true},
		{Name: "agentwisp-work-web", Path: "/code/web", Running: false},
	})

	text := out.String()
	for _, expected := range []string{"STATUS", "running", "stopped", "/code/api", "/code/web"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("output does not contain %q:\n%s", expected, text)
		}
	}
}

func TestReadTUIKey(t *testing.T) {
	tests := []struct {
		input string
		want  tuiKey
	}{
		{input: "\x1b[A", want: keyUp},
		{input: "\x1b[B", want: keyDown},
		{input: "k", want: keyUp},
		{input: "j", want: keyDown},
		{input: "\r", want: keyEnter},
		{input: "r", want: keyRefresh},
		{input: "s", want: keyStop},
		{input: "q", want: keyQuit},
	}

	for _, test := range tests {
		got, err := readTUIKey(bufio.NewReader(strings.NewReader(test.input)))
		if err != nil {
			t.Fatalf("read %q: %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("read %q: got %v, want %v", test.input, got, test.want)
		}
	}
}

func TestRenderTUI(t *testing.T) {
	var out bytes.Buffer
	renderTUI(&out, []workspace{
		{Name: "agentwisp-work-api", Path: "/code/api", Running: true},
		{Name: "agentwisp-work-web", Path: "/code/web", Running: false},
	}, 1, "", 100)

	text := out.String()
	for _, expected := range []string{"AGENTWISP", "WORKSPACE", "STATUS", "ACTION", "VOLUME", "agentwisp-work-api", "/code/api", "RUNNING", "STOPPED", "ATTACH", "START", "Open"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("output does not contain %q:\n%s", expected, text)
		}
	}
}

func TestRenderTUICompact(t *testing.T) {
	var out bytes.Buffer
	renderTUI(&out, []workspace{
		{Name: "agentwisp-work-api", Path: "/a/very/long/path/to/the/workspace", Running: true},
	}, 0, "", 40)

	for _, line := range strings.Split(out.String(), "\n") {
		if visibleWidth(line) > 40 {
			t.Fatalf("line is wider than terminal: %q", line)
		}
	}
}

func TestRenderTUIKeepsLastColumnEmpty(t *testing.T) {
	const terminalWidth = 80

	var out bytes.Buffer
	renderTUI(&out, []workspace{
		{
			Name:    "agentwisp-work-project-with-a-long-name",
			Path:    "/Users/example/projects/a-workspace-with-a-long-name",
			Running: true,
		},
	}, 0, "", terminalWidth)

	for _, line := range strings.Split(out.String(), "\n") {
		if visibleWidth(line) >= terminalWidth {
			t.Fatalf("line reaches terminal wrap column: width=%d line=%q", visibleWidth(line), line)
		}
	}
}

func TestDesiredNameWidth(t *testing.T) {
	got := desiredNameWidth([]workspace{
		{Name: "short"},
		{Name: "agentwisp-work-longer-project"},
	})
	if got != len("agentwisp-work-longer-project") {
		t.Fatalf("got %d", got)
	}
}

func TestAdaptivePanelWidth(t *testing.T) {
	workspaces := []workspace{
		{Name: "agentwisp-work-api", Path: "/code/api"},
	}

	if got := adaptivePanelWidth(workspaces, 120); got != 120 {
		t.Fatalf("panel should fill a wide terminal: got %d", got)
	}
	if got := adaptivePanelWidth(workspaces, 40); got != 40 {
		t.Fatalf("panel should fill a narrow terminal: got %d", got)
	}
}

func TestAdaptivePanelGivesVolumeMoreSpace(t *testing.T) {
	workspaces := []workspace{
		{
			Name: "agentwisp-work-api",
			Path: "/Users/example/projects/company/platform/services/api",
		},
	}

	got := adaptivePanelWidth(workspaces, 160)
	if got != 160 {
		t.Fatalf("expected panel to use terminal width, got %d", got)
	}
}

func TestRenderTitleBar(t *testing.T) {
	var out strings.Builder
	renderTitleBar(&out, []workspace{
		{Running: true},
		{Running: false},
	}, 60)

	text := out.String()
	for _, expected := range []string{"AGENTWISP", "2 total", "1 active", "1 stopped"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("title does not contain %q: %s", expected, text)
		}
	}
}

func TestWorkspaceListRowsHaveEqualWidth(t *testing.T) {
	var out strings.Builder
	renderWorkspaceList(&out, []workspace{
		{Name: "agentwisp-work-api", Path: "/code/api", Running: true},
		{Name: "agentwisp-work-much-longer-name", Path: "/code/web", Running: false},
	}, 1, 52)

	lines := strings.Split(out.String(), "\n")
	want := 52
	for i, line := range lines[:3] {
		if got := visibleWidth(line); got != want {
			t.Fatalf("line %d width: got %d, want %d: %q", i, got, want, line)
		}
	}
}

func TestWorkspaceIndex(t *testing.T) {
	workspaces := []workspace{
		{Name: "agentwisp-work-api"},
		{Name: "agentwisp-work-web"},
	}

	if got := workspaceIndex(workspaces, "agentwisp-work-web"); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
	if got := workspaceIndex(workspaces, "missing"); got != 1 {
		t.Fatalf("missing workspace fallback: got %d, want 1", got)
	}
}

func TestActionText(t *testing.T) {
	if got := actionText(true); got != "ATTACH" {
		t.Fatalf("running action: got %q", got)
	}
	if got := actionText(false); got != "START" {
		t.Fatalf("stopped action: got %q", got)
	}
}

func TestPrintUsage(t *testing.T) {
	var out bytes.Buffer
	printUsage(&out)

	text := out.String()
	for _, expected := range []string{
		"agentwisp add",
		"agentwisp start",
		"agentwisp help",
		"Workspace manager keys",
		"Stop selected workspace",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("help does not contain %q:\n%s", expected, text)
		}
	}
}

func TestNarrowWorkspaceListShowsSelectedVolume(t *testing.T) {
	var out strings.Builder
	renderWorkspaceList(&out, []workspace{
		{Name: "agentwisp-work-api", Path: "/code/api", Running: true},
	}, 0, 32)

	text := out.String()
	if !strings.Contains(text, "VOLUME") || !strings.Contains(text, "/code/api") {
		t.Fatalf("selected volume is missing:\n%s", text)
	}
}

func TestFitCell(t *testing.T) {
	if got := fitCell("api", 6); got != "api   " {
		t.Fatalf("padding: got %q", got)
	}
	if got := fitCell("very-long-name", 8); got != "very-lo…" {
		t.Fatalf("truncation: got %q", got)
	}
}

func visibleWidth(value string) int {
	const (
		plain = iota
		escape
		csi
	)
	state := plain
	width := 0
	for _, char := range value {
		switch state {
		case plain:
			if char == '\x1b' {
				state = escape
			} else {
				width++
			}
		case escape:
			if char == '[' {
				state = csi
			} else {
				state = plain
			}
		case csi:
			if char >= '@' && char <= '~' {
				state = plain
			}
		}
	}
	return width
}
