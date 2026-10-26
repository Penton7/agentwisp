package agentwisp

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// createFixture: a fake home with projects next to an existing workspace.
func createFixture(t *testing.T) (home, base string, workspaces []Workspace, tasks map[string]taskRecord) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("AGENTWISP_STATE_DIR", filepath.Join(home, ".agentwisp"))
	t.Setenv("AGENTWISP_WRITABLE", "")
	base = filepath.Join(home, "code")
	for _, dir := range []string{"api/.git", "api/.idea", "api/internal/x", "web/.git", "Zeta/.git", "notes", ".hidden/.git", "api-fix/.git", ".agentwisp"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(filepath.Join(home, ".agentwisp"), 0o700)
	if err := os.Symlink(filepath.Join(base, "web"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	workspaces = []Workspace{{Name: "agentwisp-work-web-1", Path: filepath.Join(base, "web"), Layout: 3}}
	tasks = map[string]taskRecord{filepath.Join(base, "api-fix"): {Name: "fix", Repo: filepath.Join(base, "api"), Path: filepath.Join(base, "api-fix")}}
	return home, base, workspaces, tasks
}

func candidatePaths(c *createState) []string {
	var out []string
	for _, candidate := range c.candidates {
		name := filepath.Base(candidate.path)
		if candidate.self {
			name = "."
		}
		if candidate.taken != "" {
			name += "(" + candidate.taken + ")"
		}
		out = append(out, name)
	}
	return out
}

func TestCreateBrowserShowsTheFolderAndItsSubfolders(t *testing.T) {
	home, base, workspaces, tasks := createFixture(t)
	// Started inside a repository: browsing starts at its root.
	c := newCreateState(filepath.Join(base, "api", "internal", "x"), workspaces, tasks)
	if c.dir != filepath.Join(base, "api") || !c.candidates[0].self || !c.candidates[0].git || !reflect.DeepEqual(c.preview, []string{".git", ".idea"}) {
		t.Fatalf("start %s %+v %q", c.dir, c.candidates[0], c.preview)
	}
	// A folder of projects: every subfolder, alphabetical, hidden ones and
	// symlinks left out; existing workspaces and task clones are marked.
	c = newCreateState(base, workspaces, tasks)
	want := []string{".", "api", "api-fix(task clone)", "notes", "web(has a workspace)", "Zeta"}
	if got := candidatePaths(c); !reflect.DeepEqual(got, want) {
		t.Fatalf("listing %q", got)
	}
	// Right opens, left goes back up with the folder still selected.
	typeText(c, "j\x1b[C")
	if c.dir != filepath.Join(base, "api") {
		t.Fatalf("right: %s", c.dir)
	}
	typeText(c, "\x1b[D")
	if c.dir != base || filepath.Base(c.candidates[c.selected].path) != "api" {
		t.Fatalf("left: %s %d", c.dir, c.selected)
	}
	// Home can be browsed (to reach projects) but not created.
	c = newCreateState(home, workspaces, tasks)
	// Home itself cannot be created, so the first subfolder is selected.
	if c.dir != home || c.selected != 1 || filepath.Base(c.candidates[1].path) != "code" {
		t.Fatalf("%s %d", c.dir, c.selected)
	}
	if action := typeText(c, "k\r"); action.kind != createNone || !strings.Contains(c.problem, "home directory") {
		t.Fatalf("home created: %+v %q", action, c.problem)
	}
	// Up from home is allowed too, e.g. to reach /Volumes.
	typeText(c, "h")
	if c.dir != filepath.Dir(home) {
		t.Fatalf("up from home: %s", c.dir)
	}
}

func TestValidateCreatePathRefusesWideMounts(t *testing.T) {
	home, base, _, _ := createFixture(t)
	if got, err := validateCreatePath("~/code/api/"); err != nil || got != filepath.Join(base, "api") {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := validateCreatePath(filepath.Join(base, "link")); err != nil || got != filepath.Join(base, "web") {
		t.Fatalf("symlink not resolved: %q %v", got, err)
	}
	os.WriteFile(filepath.Join(base, "file"), nil, 0o644)
	for _, bad := range []string{"", "relative", "~", home, "/", filepath.Dir(home), filepath.Join(home, ".agentwisp"), filepath.Join(base, "file"), filepath.Join(base, "missing")} {
		if _, err := validateCreatePath(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestCompleteDirectory(t *testing.T) {
	_, base, _, _ := createFixture(t)
	os.WriteFile(filepath.Join(base, "apiary.txt"), nil, 0o644)
	if got, options := completeDirectory("~/code/we"); got != "~/code/web/" || options != nil {
		t.Fatalf("%q %q", got, options)
	}
	// Ambiguous: common prefix and the choices; files and hidden entries are not offered.
	if got, options := completeDirectory("~/code/a"); got != "~/code/api" || !reflect.DeepEqual(options, []string{"api", "api-fix"}) {
		t.Fatalf("%q %q", got, options)
	}
	if got, options := completeDirectory("~/code/."); got != "~/code/." || !reflect.DeepEqual(options, []string{".agentwisp", ".hidden"}) {
		t.Fatalf("hidden %q %q", got, options)
	}
	if got, _ := completeDirectory("nothing"); got != "nothing" {
		t.Fatal(got)
	}
}

func typeText(c *createState, text string) createAction {
	var action createAction
	reader := bufio.NewReader(strings.NewReader(text))
	for {
		ev, err := readKeyEvent(reader)
		if err != nil {
			return action
		}
		if action = c.handle(ev); action.kind != createNone {
			return action
		}
	}
}

func TestCreateScreenFlow(t *testing.T) {
	_, base, workspaces, tasks := createFixture(t)
	c := newCreateState(base, workspaces, tasks)

	// enter / x act on the selected row: this folder, or a subfolder.
	typeText(c, "j")
	if action := typeText(c, "\r"); action.kind != createProject || action.path != filepath.Join(base, "api") || action.agent != "" {
		t.Fatalf("enter %+v", action)
	}
	if action := typeText(c, "x"); action.kind != createProject || action.agent != "codex" {
		t.Fatalf("x %+v", action)
	}
	// A folder that already has a workspace is selected, not created again.
	typeText(c, "jjj")
	if action := typeText(c, "\r"); action.kind != createSelect || action.path != filepath.Join(base, "web") {
		t.Fatalf("taken %+v", action)
	}
	// notes has no .git: t explains instead of asking for a name.
	c = newCreateState(base, workspaces, tasks)
	typeText(c, "jjj")
	if typeText(c, "t"); c.step != createPick || !strings.Contains(c.problem, "needs a git repository") {
		t.Fatalf("task from non-git: %q", c.problem)
	}

	// "/" types a folder to go to; shortcut letters are text there.
	typeText(c, "/")
	if c.step != createPath || string(c.input) != homeRelative(base)+"/" {
		t.Fatalf("path start %q", string(c.input))
	}
	typeText(c, "\x15~/code/nox\x7ftes-jkqc\x7f\x7f\x7f\x7f\x7f\r")
	if c.step != createPick || c.dir != filepath.Join(base, "notes") || !c.candidates[0].self {
		t.Fatalf("go to: step %d dir %s", c.step, c.dir)
	}
	typeText(c, "/~/code/missing\r")
	if c.step != createPath || !strings.Contains(c.problem, "not a directory") {
		t.Fatalf("missing path: %q", c.problem)
	}
	typeText(c, "\x1b")
	if c.step != createPick {
		t.Fatal("esc did not leave path input")
	}

	// Task: name is normalized as typed and validated on enter.
	c = newCreateState(filepath.Join(base, "api"), workspaces, tasks)
	typeText(c, "t")
	if c.step != createName {
		t.Fatal("t did not ask for a name")
	}
	if action := typeText(c, "Fix Login!\r"); action.kind != createTask || action.name != "fix-login" || action.path != filepath.Join(base, "api") {
		t.Fatalf("task %+v", action)
	}
	c.input = nil
	if action := typeText(c, "fix\r"); action.kind != createNone || !strings.Contains(c.problem, "already exists") {
		t.Fatalf("existing clone accepted: %+v %q", action, c.problem)
	}
	if action := typeText(c, "\x1b\x1b"); action.kind != createBack {
		t.Fatalf("esc esc %+v", action)
	}
}

func TestReadKeyEventText(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("é\x7f\t\x15\x1b[C\x1b[1;5Dq"))
	var got []keyEvent
	for {
		ev, err := readKeyEvent(reader)
		if err != nil {
			break
		}
		got = append(got, ev)
	}
	want := []keyEvent{{key: keyUnknown, ch: 'é'}, {key: keyBackspace}, {key: keyTab}, {key: keyClear}, {key: keyRight}, {}, {key: keyQuit, ch: 'q'}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestCreateScreenFitsAllSizes(t *testing.T) {
	_, base, workspaces, tasks := createFixture(t)
	for i := 0; i < 30; i++ {
		os.MkdirAll(filepath.Join(base, "repo-with-a-rather-long-name-"+string(rune('a'+i%26))+string(rune('a'+i/26)), ".git"), 0o755)
	}
	states := map[string]func() *createState{
		"pick": func() *createState { return newCreateState(filepath.Join(base, "api"), workspaces, tasks) },
		"last": func() *createState {
			c := newCreateState(base, workspaces, tasks)
			c.selected = len(c.candidates) - 1
			c.problem = "Create failed: " + strings.Repeat("long error ", 20)
			return c
		},
		"path": func() *createState {
			c := newCreateState(base, workspaces, tasks)
			c.startPath("")
			_, c.completions = completeDirectory("~/code/r")
			return c
		},
		"name": func() *createState {
			c := newCreateState(filepath.Join(base, "api"), workspaces, tasks)
			c.step, c.input = createName, []rune("x\x1b]evil")
			return c
		},
	}
	for name, make := range states {
		for _, st := range []tuiStyle{plainStyle(), colorStyle(darkPalette), {mode: color256, pal: lightPalette, g: asciiGlyphs}} {
			for _, width := range []int{2, 20, 45, 80, 134} {
				for _, height := range []int{0, 10, 30} {
					view := tuiView{create: make(), width: width, height: height, now: tuiNow, style: st, message: "Creating api..."}
					lines := renderLines(view)
					for i, line := range lines {
						if w := visibleWidth(line); w >= width && width > 1 {
							t.Fatalf("%s %dx%d line %d too wide (%d): %q", name, width, height, i, w, line)
						}
						if strings.Contains(line, "\x1b]") {
							t.Fatalf("%s: unescaped OSC in %q", name, line)
						}
					}
					if height > 0 && len(lines) > height+1 {
						t.Fatalf("%s %dx%d: %d lines", name, width, height, len(lines))
					}
				}
			}
		}
	}
}

func TestProtectionSummary(t *testing.T) {
	out := "Creating x...\nRead-only in the workspace (host tools run them): .git, .idea\nThe agent can read history...\n"
	if list, warn := protectionSummary(out); list != ".git, .idea" || warn {
		t.Fatalf("%q %v", list, warn)
	}
	if _, warn := protectionSummary("Note: the runtime cannot mount single files read-only (x); writable: .envrc\n"); !warn {
		t.Fatal("fallback not reported")
	}
}
