package agentwisp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The new-workspace screen (n in the manager) is a folder browser that starts
// where agw was started: the first row is that folder itself, the others are
// its subfolders. Arrows walk the tree (right opens, left goes up), and the
// selected row is what gets created:
//
//	enter  project in place      c / x  project, then start the agent
//	t      isolated task (git)   /  ~   type a path to jump to
//
// The browser only reads directories; it never runs git, because a folder
// may be an agent-controlled clone.

type createStep int

const (
	createPick createStep = iota
	createPath
	createName
)

type createCandidate struct {
	path  string
	self  bool   // The browsed folder itself ("this folder").
	git   bool   // Has .git: can be the source of a task.
	taken string // Already a workspace or task clone; creating selects it.
}

type createState struct {
	step        createStep
	dir         string // The folder being browsed.
	candidates  []createCandidate
	selected    int
	input       []rune
	completions []string
	preview     []string // Paths that would be read-only for the agent.
	problem     string
	taken       map[string]string // Workspace and task clone paths.
	truncated   bool              // The folder has more subfolders than shown.
}

type createActionKind int

const (
	createNone createActionKind = iota
	createBack
	createProject
	createTask
	createSelect // The folder already has a workspace: select it in the list.
)

type createAction struct {
	kind  createActionKind
	path  string
	name  string // Task name.
	agent string // Start this agent after creating a project.
}

const maxBrowseEntries = 500

func newCreateState(cwd string, workspaces []Workspace, tasks map[string]taskRecord) *createState {
	c := &createState{taken: map[string]string{}}
	for _, item := range workspaces {
		if filepath.IsAbs(item.Path) {
			c.taken[canonicalPath(item.Path)] = "has a workspace"
		}
	}
	for path := range tasks {
		c.taken[canonicalPath(path)] = "task clone"
	}
	start := cwd
	if start == "" {
		start, _ = os.UserHomeDir()
	}
	// Inside a repository, start at its root: that is what gets mounted.
	c.browse(repositoryRoot(canonicalPath(start)), "")
	return c
}

// browse lists dir; focus selects that subfolder (coming back up from it).
func (c *createState) browse(dir, focus string) {
	c.dir, c.selected, c.problem = dir, 0, ""
	c.candidates = []createCandidate{c.candidate(dir, true)}
	f, err := os.Open(dir)
	if err != nil {
		c.problem = "cannot read " + homeRelative(dir)
		c.refreshPreview()
		return
	}
	entries, _ := f.ReadDir(maxBrowseEntries + 1)
	f.Close()
	c.truncated = len(entries) > maxBrowseEntries
	var names []string
	for _, entry := range entries[:min(len(entries), maxBrowseEntries)] {
		// Symlinks report !IsDir and are not followed; hidden folders are
		// configuration, not projects.
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			names = append(names, entry.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	if _, err := validateCreatePath(dir); err != nil && focus == "" && len(names) > 0 {
		focus = filepath.Join(dir, names[0]) // This folder cannot be created (home): start on a subfolder.
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		c.candidates = append(c.candidates, c.candidate(path, false))
		if path == focus {
			c.selected = len(c.candidates) - 1
		}
	}
	c.refreshPreview()
}

func (c *createState) candidate(path string, self bool) createCandidate {
	return createCandidate{path: path, self: self, git: hasGitEntry(path), taken: c.taken[path]}
}

func (c *createState) current() (createCandidate, bool) {
	if c.selected >= 0 && c.selected < len(c.candidates) {
		return c.candidates[c.selected], true
	}
	return createCandidate{}, false
}

func (c *createState) refreshPreview() {
	c.preview = nil
	if candidate, ok := c.current(); ok {
		_, c.preview, _, _ = workspaceProtection(candidate.path, false, os.Getenv)
	}
}

// handle applies one key and says whether the manager must act.
func (c *createState) handle(ev keyEvent) createAction {
	switch c.step {
	case createPath:
		return c.handlePath(ev)
	case createName:
		return c.handleName(ev)
	}
	c.problem = ""
	candidate, ok := c.current()
	switch ev.key {
	case keyEscape, keyQuit:
		return createAction{kind: createBack}
	case keyUp, keyDown:
		step := 1
		if ev.key == keyUp {
			step = len(c.candidates) - 1
		}
		c.selected = (c.selected + step) % len(c.candidates)
		c.refreshPreview()
	case keyRight:
		if ok && !candidate.self {
			c.browse(candidate.path, "")
		}
	case keyLeft, keyBackspace:
		if parent := filepath.Dir(c.dir); parent != c.dir {
			c.browse(parent, c.dir)
		}
	case keySlash:
		c.startPath("")
	case keyEnter, keyClaude, keyCodex:
		if !ok {
			return createAction{}
		}
		if candidate.taken != "" {
			return createAction{kind: createSelect, path: candidate.path}
		}
		if _, err := validateCreatePath(candidate.path); err != nil {
			c.problem = err.Error()
			return createAction{}
		}
		action := createAction{kind: createProject, path: candidate.path}
		if ev.key == keyClaude {
			action.agent = string(ProviderClaude)
		} else if ev.key == keyCodex {
			action.agent = string(ProviderCodex)
		}
		return action
	case keyTask:
		switch {
		case !ok || !candidate.git:
			c.problem = "a task needs a git repository; " + homeRelative(candidate.path) + " has no .git"
		case candidate.taken == "task clone":
			c.problem = "that is a task clone; start tasks from the main repository"
		default:
			c.step, c.input = createName, nil
		}
	default:
		if ev.ch == '~' {
			c.startPath("~")
		}
	}
	return createAction{}
}

// startPath opens the input line for a folder to jump to.
func (c *createState) startPath(initial string) {
	c.step, c.completions, c.problem = createPath, nil, ""
	if initial == "" {
		initial = homeRelative(c.dir) + "/"
	}
	c.input = []rune(initial)
}

func (c *createState) handlePath(ev keyEvent) createAction {
	switch ev.key {
	case keyEscape:
		c.step, c.problem, c.completions = createPick, "", nil
		return createAction{}
	case keyBackspace:
		if len(c.input) > 0 {
			c.input = c.input[:len(c.input)-1]
		}
	case keyClear:
		c.input = nil
	case keyTab:
		completed, options := completeDirectory(string(c.input))
		c.input, c.completions = []rune(completed), options
		return createAction{}
	case keyEnter:
		path := expandHome(strings.TrimSpace(string(c.input)))
		if !filepath.IsAbs(path) {
			c.problem = "use an absolute path or ~/..."
			return createAction{}
		}
		path = canonicalPath(path)
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			c.problem = homeRelative(path) + " is not a directory"
			return createAction{}
		}
		c.step, c.completions = createPick, nil
		c.browse(path, "")
		return createAction{}
	default:
		if ev.ch != 0 && len(c.input) < 1024 {
			c.input = append(c.input, ev.ch)
		}
	}
	c.problem, c.completions = "", nil
	return createAction{}
}

func (c *createState) handleName(ev keyEvent) createAction {
	switch ev.key {
	case keyEscape:
		c.step, c.problem = createPick, ""
		return createAction{}
	case keyBackspace:
		if len(c.input) > 0 {
			c.input = c.input[:len(c.input)-1]
		}
	case keyClear:
		c.input = nil
	case keyEnter:
		name := string(c.input)
		if err := validateTaskName(name); err != nil {
			c.problem = "use lowercase letters, digits and '-' (max 40)"
			return createAction{}
		}
		candidate, _ := c.current()
		if _, err := os.Lstat(taskClonePath(candidate.path, name)); err == nil {
			c.problem = homeRelative(taskClonePath(candidate.path, name)) + " already exists"
			return createAction{}
		}
		return createAction{kind: createTask, path: candidate.path, name: name}
	default:
		r := ev.ch
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if r == ' ' || r == '_' {
			r = '-'
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			if len(c.input) < 40 {
				c.input = append(c.input, r)
			}
		}
	}
	c.problem = ""
	return createAction{}
}

func hasGitEntry(path string) bool {
	_, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil
}

// repositoryRoot walks up to the nearest directory with .git, without
// running git; a directory outside any repository is its own root.
func repositoryRoot(dir string) string {
	home, _ := os.UserHomeDir()
	for current := dir; ; current = filepath.Dir(current) {
		if hasGitEntry(current) {
			return current
		}
		if current == home || filepath.Dir(current) == current {
			return dir
		}
	}
}

func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// validateCreatePath accepts an existing directory that is safe to mount:
// never /, the home directory or one of its ancestors (that would hand the
// agent every project, SSH key and token), nor AgentWisp's own state.
func validateCreatePath(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", errors.New("type a directory")
	}
	path := expandHome(input)
	if !filepath.IsAbs(path) {
		return "", errors.New("use an absolute path or ~/...")
	}
	path = canonicalPath(path)
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", homeRelative(path))
	}
	if strings.ContainsAny(path, ":,\r\n") {
		return "", errors.New("paths with ':' or ',' cannot be mounted")
	}
	if home, err := os.UserHomeDir(); err == nil {
		home = canonicalPath(home)
		if path == home || strings.HasPrefix(home, path+string(filepath.Separator)) || filepath.Dir(path) == path {
			return "", fmt.Errorf("%s would expose your whole home directory; choose a project inside it", homeRelative(path))
		}
	}
	state := canonicalPath(agentWispStateDir())
	if path == state || strings.HasPrefix(path, state+string(filepath.Separator)) {
		return "", errors.New("that is AgentWisp's own state directory")
	}
	return path, nil
}

// completeDirectory completes the last path element to a directory name.
// It returns the new input and, when ambiguous, the matching names.
func completeDirectory(input string) (string, []string) {
	dirPart, prefix := input, ""
	if i := strings.LastIndex(input, "/"); i >= 0 {
		dirPart, prefix = input[:i+1], input[i+1:]
	} else {
		return input, nil
	}
	f, err := os.Open(expandHome(dirPart))
	if err != nil {
		return input, nil
	}
	defer f.Close()
	entries, _ := f.ReadDir(1000)
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".")) {
			continue
		}
		if info, err := os.Stat(filepath.Join(expandHome(dirPart), name)); err == nil && info.IsDir() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	switch len(names) {
	case 0:
		return input, nil
	case 1:
		return dirPart + names[0] + "/", nil
	}
	common := names[0]
	for _, name := range names[1:] {
		for !strings.HasPrefix(name, common) {
			common = common[:len(common)-1]
		}
	}
	return dirPart + common, names
}

// protectionSummary turns the creation output into one manager message.
func protectionSummary(output string) (string, bool) {
	var protected string
	warn := false
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "Read-only in the workspace"):
			_, list, _ := strings.Cut(line, ": ")
			protected = list
		case strings.HasPrefix(line, "Warning:"), strings.Contains(line, "cannot mount single files"):
			warn = true
		}
	}
	return protected, warn
}

// renderCreate is the body of the new-workspace screen.
func renderCreate(view tuiView, l tuiLayout) []string {
	st := view.style
	c := view.create
	width := l.content
	if c.step == createName {
		return renderTaskName(c, st, width)
	}
	out := []string{"", st.line([]span{{"  NEW WORKSPACE", roleHeading}, {"  in ", roleMeta}, {homeRelative(c.dir), roleStrong}}, width)}

	nameWidth := 11 // "this folder"
	for _, candidate := range c.candidates[1:] {
		nameWidth = max(nameWidth, min(32, len([]rune(terminalText(filepath.Base(candidate.path))))))
	}
	// Window the rows so the description below always fits.
	visible := len(c.candidates)
	if l.bodyHeight > 0 {
		visible = max(1, min(visible, l.bodyHeight-11))
	}
	first := 0
	if c.selected >= visible {
		first = c.selected - visible + 1
	}
	last := min(len(c.candidates), first+visible)
	if first > 0 {
		out = append(out, st.line([]span{{fmt.Sprintf("      ^ %d more", first), roleFaint}}, width))
	}
	for i := first; i < last; i++ {
		out = append(out, renderCandidate(st, c.candidates[i], i == c.selected && c.step == createPick, nameWidth, width))
	}
	if more := len(c.candidates) - last; more > 0 || c.truncated {
		text := fmt.Sprintf("      v %d more", more)
		if c.truncated {
			text += fmt.Sprintf(" (first %d entries; type a path with /)", maxBrowseEntries)
		}
		out = append(out, st.line([]span{{text, roleFaint}}, width))
	}
	if len(c.candidates) == 1 {
		out = append(out, st.line([]span{{"      no subfolders", roleFaint}}, width))
	}
	if c.step == createPath {
		out = append(out, "", st.line([]span{{"  ", roleText}, {st.g.bar, roleAccent}, {" go to ", roleDim}, {string(c.input), roleStrong}, {st.g.cursor, roleAccent}}, width))
		if len(c.completions) > 0 {
			shown := strings.Join(c.completions[:min(len(c.completions), 12)], "  ")
			if len(c.completions) > 12 {
				shown += fmt.Sprintf("  (+%d)", len(c.completions)-12)
			}
			out = append(out, st.line([]span{{"        " + shown, roleDim}}, width))
		}
	}
	out = append(out, "")

	// What the keys do for the selected row, as a small table.
	choice := func(key string, keyRole tuiRole, what string, whatRole tuiRole, rest ...span) []span {
		return append([]span{{"  ", roleText}, {fmt.Sprintf("%-7s", key), keyRole}, {fmt.Sprintf("%-10s", what), whatRole}}, rest...)
	}
	const indent = "                   "
	var desc [][]span
	candidate, _ := c.current()
	_, invalid := validateCreatePath(candidate.path)
	switch {
	case c.step == createPath:
		desc = append(desc, choice("tab", roleStrong, "complete", roleText), choice("enter", roleKeyMain, "go there", roleText),
			choice("ctrl-u", roleStrong, "clear", roleText), choice("esc", roleStrong, "back", roleText))
	case candidate.taken != "":
		desc = append(desc, choice("enter", roleKeyMain, "select", roleStrong, span{homeRelative(candidate.path) + " " + candidate.taken + "; enter selects it", roleDim}))
	case invalid != nil:
		desc = append(desc, []span{{"  " + invalid.Error(), roleWarn}},
			choice("→", roleStrong, "open", roleText, span{"a project folder below, or ", roleDim}, span{"/", roleStrong}, span{" to type a path", roleDim}))
	default:
		protected := "nothing yet (no .git, IDE or agent config)"
		if len(c.preview) > 0 {
			protected = strings.Join(c.preview, " ")
		}
		desc = append(desc,
			choice("enter", roleKeyMain, "project", roleStrong, span{"the agent edits ", roleDim}, span{homeRelative(candidate.path), roleText}, span{" in place", roleDim}),
			[]span{{indent + "read-only for it: ", roleDim}, {protected, roleText}})
		if contains(c.preview, ".git") {
			desc = append(desc, []span{{indent + "you keep committing on the host (PyCharm, git)", roleDim}})
		}
		desc = append(desc, choice("c / x", roleKeyAgent, "+ agent", roleStrong, span{"same, then start Claude Code / Codex in it", roleDim}))
		if candidate.git {
			desc = append(desc, choice("t", roleKeyAgent, "task", roleStrong, span{"isolated clone on branch agw/<name>; the agent commits, ", roleDim},
				span{"f", roleKeySafe}, span{" fetches it back", roleDim}))
		}
	}
	for _, line := range desc {
		out = append(out, st.line(line, width))
	}
	if c.problem != "" {
		out = append(out, "", st.line([]span{{"  " + c.problem, roleError}}, width))
	}
	return out
}

func renderTaskName(c *createState, st tuiStyle, width int) []string {
	candidate, _ := c.current()
	name := string(c.input)
	clone := homeRelative(taskClonePath(candidate.path, "<name>"))
	branch := "agw/<name>"
	if name != "" {
		clone, branch = homeRelative(taskClonePath(candidate.path, name)), "agw/"+name
	}
	lines := [][]span{nil,
		{{"  NEW TASK", roleHeading}, {"  from ", roleMeta}, {homeRelative(candidate.path), roleText}},
		nil,
		{{"  name    ", roleDim}, {name, roleStrong}, {st.g.cursor, roleAccent}},
		{{"  clone   ", roleDim}, {clone, roleText}},
		{{"  branch  ", roleDim}, {branch, roleOK}, {" from the current HEAD", roleMeta}},
		nil,
		{{"  The agent works and commits in the clone; ", roleDim}, {"f", roleKeySafe}, {" fetches the branch back here.", roleDim}},
		{{"  Do not open the clone in an IDE: its .git belongs to the agent.", roleDim}},
	}
	if c.problem != "" {
		lines = append(lines, nil, []span{{"  " + c.problem, roleError}})
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, st.line(line, width))
	}
	return out
}

func renderCandidate(st tuiStyle, candidate createCandidate, selected bool, nameWidth, width int) string {
	var bg *rgb
	if selected && !st.plain() {
		bg = &st.pal.selBG
	}
	marker := []span{{"   ", roleText}}
	switch {
	case selected && st.plain():
		marker = []span{{" > ", roleText}}
	case selected:
		marker = []span{{"  ", roleText}, {st.g.bar, roleAccent}}
	}
	glyph, glyphRole := st.g.ring, roleFaint
	if candidate.git {
		glyph, glyphRole = st.g.dot, roleAccent
	}
	name, nameRole := filepath.Base(candidate.path)+"/", roleStrong
	if candidate.self {
		name = "this folder"
	}
	if candidate.taken != "" {
		nameRole = roleDim
	}
	spans := append(marker, span{" " + glyph + " ", glyphRole}, span{fmt.Sprintf("%-*s", nameWidth+1, truncateCell(name, nameWidth+1)), nameRole})
	var tags []span
	if candidate.git {
		tags = append(tags, span{"git", roleAccent})
	}
	if candidate.taken != "" {
		tags = append(tags, span{candidate.taken, roleOK})
	}
	if candidate.self {
		tags = append(tags, span{homeRelative(candidate.path), roleDim})
	}
	for i, tag := range tags {
		sep := "  "
		if i > 0 {
			sep = " · "
		}
		spans = append(spans, span{sep, roleFaint}, tag)
	}
	return st.lineOn(spans, width, bg, true)
}

func createKeyHints(c *createState) []keyHint {
	switch c.step {
	case createPath:
		return []keyHint{{"enter", "go there", roleKeyMain}, {"tab", "complete", roleStrong}, {"esc", "back", roleStrong}}
	case createName:
		return []keyHint{{"enter", "create task", roleKeyMain}, {"esc", "back", roleStrong}}
	}
	var hints []keyHint
	if candidate, ok := c.current(); ok {
		_, invalid := validateCreatePath(candidate.path)
		switch {
		case candidate.taken != "":
			hints = append(hints, keyHint{"enter", "select", roleKeyMain})
		case invalid == nil:
			hints = append(hints, keyHint{"enter", "create", roleKeyMain})
		}
		if invalid != nil {
			candidate.taken = "invalid" // No create keys below.
		}
		if candidate.git && candidate.taken == "" {
			hints = append(hints, keyHint{"t", "task", roleKeyAgent})
		}
		if candidate.taken == "" {
			hints = append(hints, keyHint{"c", "+claude", roleKeyAgent}, keyHint{"x", "+codex", roleKeyAgent})
		}
		if !candidate.self {
			hints = append(hints, keyHint{"→", "open", roleStrong})
		}
	}
	return append(hints, keyHint{"←", "up", roleStrong}, keyHint{"/", "go to", roleStrong}, keyHint{"esc", "back", roleStrong})
}
