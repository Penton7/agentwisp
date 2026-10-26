package agentwisp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

// managerState survives between manager screens: after a shell or agent
// started from the manager exits, the manager reopens on the same row.
type managerState struct {
	selectedName string
	message      string
	messageRole  tuiRole
}

func chooseWorkspace(service *WorkspaceService, in io.Reader, out, errOut io.Writer) error {
	if in != os.Stdin || out != os.Stdout {
		return chooseWorkspaceByNumber(service, in, out, errOut)
	}
	state := managerState{}
	cwd, _ := os.Getwd()
	for {
		action, err := runManager(service, in, out, &state, cwd)
		if errors.Is(err, errNoTerminal) {
			return chooseWorkspaceByNumber(service, in, out, errOut)
		}
		if err != nil || action == nil {
			return err
		}
		// The terminal is restored before the session runs.
		state.message = ""
		if err := action(); err != nil {
			state.message, state.messageRole = "Session ended with an error: "+err.Error(), roleError
		}
	}
}

var errNoTerminal = errors.New("no interactive terminal")

// cachedManagerStyle keeps one theme per process: the manager reopens after
// every session, and the terminal background does not change meanwhile.
var cachedManagerStyle *tuiStyle

// taskIndex maps task clone paths to their records for labels and actions.
func taskIndex() map[string]taskRecord {
	return loadAllTasks(&doctorReport{})
}

// runManager shows the manager until the user quits (nil action) or picks a
// session to run (returned action runs after the terminal is restored).
func runManager(service *WorkspaceService, in io.Reader, out io.Writer, state *managerState, cwd string) (func() error, error) {
	term, err := enterRawTerminal()
	if err != nil {
		return nil, errNoTerminal
	}
	defer term.restore()
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM, syscall.SIGWINCH)
	defer signal.Stop(interrupts)

	workspaces, err := service.List()
	if err != nil {
		return nil, err
	}
	tasks := taskIndex()
	agents := workspaceAgents(workspaces)
	if cachedManagerStyle == nil {
		// Probe the background before alternate screen and key reader start.
		style := managerStyle(probeTerminalBackground)
		cachedManagerStyle = &style
	}
	view := tuiView{message: state.message, messageRole: state.messageRole, now: time.Now(), style: *cachedManagerStyle}
	view.height, view.width = terminalSize()
	rebuild := func() {
		name := state.selectedName
		view.rows = buildTUIRows(workspaces, agents, tasks)
		view.selected = rowIndex(view.rows, name)
		if len(view.rows) > 0 {
			state.selectedName = view.rows[view.selected].ws.Name
		}
	}
	refreshDetails := func() {
		view.detail = nil
		if len(view.rows) > 0 {
			view.detail = tuiDetails(view.rows[view.selected], time.Now())
		}
	}
	rebuild()
	refreshDetails()

	reader := bufio.NewReader(in)
	keyCh := make(chan keyEvent, 1)
	errCh := make(chan error, 1)
	readingKey := false
	ticker := time.NewTicker(agentStatusPoll)
	defer ticker.Stop()
	ticks := 0
	var pendingDelete *tuiRow
	last := ""
	draw := func() {
		var frame strings.Builder
		if pendingDelete != nil {
			var buf bytes.Buffer
			renderDeleteConfirmation(&buf, *pendingDelete, view.width, view.style)
			frame.WriteString(buf.String())
		} else {
			view.now = time.Now()
			view.offset = scrollOffset(view)
			renderTUIView(&frame, view)
		}
		// Redraw only when the frame changed: avoids flicker on periodic polls.
		if frame.String() != last {
			last = frame.String()
			fmt.Fprint(out, last)
		}
	}
	setMessage := func(message string, role tuiRole) {
		view.message, view.messageRole = message, role
		state.message, state.messageRole = message, role
	}

	for {
		draw()
		if !readingKey {
			readingKey = true
			go func() {
				key, readErr := readKeyEvent(reader)
				if readErr != nil {
					errCh <- readErr
					return
				}
				keyCh <- key
			}()
		}

		select {
		case <-ticker.C:
			ticks++
			// Agent states every tick; container states and details less often.
			if ticks%5 == 0 {
				if refreshed, err := service.List(); err == nil {
					workspaces = refreshed
					tasks = taskIndex()
				}
			}
			agents = workspaceAgents(workspaces)
			rebuild()
			if ticks%5 == 0 {
				refreshDetails()
			}
			continue
		case received := <-interrupts:
			if received == syscall.SIGWINCH {
				view.height, view.width = terminalSize()
				last = ""
				continue
			}
			return nil, nil
		case readErr := <-errCh:
			readingKey = false
			if errors.Is(readErr, io.EOF) {
				return nil, nil
			}
			return nil, readErr
		case event := <-keyCh:
			readingKey = false
			if view.create != nil {
				action := view.create.handle(event)
				switch action.kind {
				case createBack:
					view.create = nil
					setMessage("", roleText)
				case createSelect:
					view.create = nil
					for _, item := range workspaces {
						if filepath.IsAbs(item.Path) && canonicalPath(item.Path) == action.path {
							state.selectedName = item.Name
						}
					}
					rebuild()
					refreshDetails()
					setMessage(homeRelative(action.path)+" already has a workspace; selected it", roleDim)
				case createProject, createTask:
					run, err := createFromManager(service, action, &view, &workspaces, &tasks, state, draw, setMessage)
					agents = workspaceAgents(workspaces)
					rebuild()
					refreshDetails()
					if err == nil && run != nil {
						return run, nil
					}
				}
				continue
			}
			key := event.key
			if key == keyEscape {
				key = keyQuit
			}
			if view.help {
				view.help = false
				continue
			}
			if pendingDelete != nil {
				chosen := *pendingDelete
				pendingDelete = nil
				if key != keyConfirm {
					setMessage("Removal cancelled", roleDim)
					continue
				}
				if err := service.Remove(chosen.ws.Name, true); err != nil {
					setMessage("Removal failed: "+err.Error(), roleError)
					continue
				}
				removed := "Removed " + chosen.label + "; host files, logs and saved logins kept"
				if chosen.task != nil {
					removed += "; clone kept (agw task rm " + chosen.task.Name + ")"
				}
				setMessage(removed, roleOK)
				if refreshed, err := service.List(); err == nil {
					workspaces = refreshed
				} else {
					workspaces = withoutWorkspace(workspaces, chosen.ws.Name)
				}
				agents = workspaceAgents(workspaces)
				rebuild()
				refreshDetails()
				continue
			}
			setMessage("", roleText)
			switch key {
			case keyQuit:
				return nil, nil
			case keyHelp:
				view.help = true
				continue
			case keyNew:
				view.create = newCreateState(cwd, workspaces, tasks)
				continue
			case keyRefresh:
				refreshed, err := service.List()
				if err != nil {
					setMessage("Refresh failed: "+err.Error(), roleError)
					continue
				}
				workspaces, tasks = refreshed, taskIndex()
				agents = workspaceAgents(workspaces)
				rebuild()
				refreshDetails()
				setMessage("Refreshed", roleDim)
				continue
			}
			if len(view.rows) == 0 {
				continue
			}
			row := view.rows[view.selected]
			switch key {
			case keyUp, keyDown:
				step := 1
				if key == keyUp {
					step = len(view.rows) - 1
				}
				view.selected = (view.selected + step) % len(view.rows)
				state.selectedName = view.rows[view.selected].ws.Name
				refreshDetails()
			case keyDelete:
				pendingDelete = &row
			case keyStop:
				if !row.ws.Running {
					setMessage(row.label+" is already stopped", roleWarn)
					continue
				}
				setMessage("Stopping "+row.label+"...", roleAccent)
				draw()
				if err := service.Stop(row.ws.Name); err != nil {
					setMessage("Stop failed: "+err.Error(), roleError)
				} else {
					setMessage(row.label+" stopped; proxy stopped", roleOK)
				}
				if refreshed, err := service.List(); err == nil {
					workspaces = refreshed
				}
				agents = workspaceAgents(workspaces)
				rebuild()
				refreshDetails()
			case keyFetch:
				if row.task == nil {
					setMessage("f works for agw task workspaces; "+row.label+" is a project", roleWarn)
					continue
				}
				setMessage("Fetching "+row.task.Branch+"...", roleAccent)
				draw()
				var output bytes.Buffer
				if err := service.FetchTask(row.task.Repo, row.task.Name, &output); err != nil {
					setMessage("Fetch failed: "+err.Error(), roleError)
				} else {
					setMessage(lastLine(output.String()), roleOK)
				}
				refreshDetails()
			case keyEnter, keyClaude, keyCodex:
				name, label := row.ws.Name, row.label
				state.selectedName = name
				var command []string
				switch key {
				case keyClaude:
					command = []string{"claude"}
				case keyCodex:
					command = []string{"codex"}
				}
				return func() error {
					what := "shell"
					if command != nil {
						what = command[0]
					}
					fmt.Fprintf(out, "agw: %s in %s; exit it to return to the manager.\n", what, terminalText(label))
					if command == nil {
						return service.Enter(name, out, os.Stderr)
					}
					return service.RunAgent(name, command, out, os.Stderr)
				}, nil
			}
		}
	}
}

// createFromManager creates a project workspace or a task while the manager
// stays on screen. On success the new workspace is selected; with an agent
// requested, the returned action starts it after the terminal is restored.
func createFromManager(service *WorkspaceService, action createAction, view *tuiView, workspaces *[]Workspace,
	tasks *map[string]taskRecord, state *managerState, draw func(), setMessage func(string, tuiRole)) (func() error, error) {
	var output bytes.Buffer
	var name, label string
	var err error
	switch action.kind {
	case createTask:
		label = filepath.Base(action.path) + " / " + action.name
		setMessage("Cloning "+homeRelative(action.path)+" and creating "+label+"...", roleAccent)
		draw()
		err = service.NewTask(action.path, action.name, "HEAD", &output)
		name = workspaceContainerName(taskClonePath(action.path, action.name))
	default:
		label = filepath.Base(action.path)
		name = workspaceContainerName(action.path)
		for _, item := range *workspaces {
			if item.Name == name {
				view.create = nil
				state.selectedName = name
				setMessage(label+" already has a workspace; selected it", roleDim)
				return nil, nil
			}
		}
		setMessage("Creating "+label+" (first time may pull the image)...", roleAccent)
		draw()
		err = service.Ensure(name, action.path, &output)
	}
	if err != nil {
		view.create.problem = "Create failed: " + strings.TrimSpace(err.Error())
		setMessage("", roleText)
		return nil, err
	}
	view.create = nil
	if refreshed, listErr := service.List(); listErr == nil {
		*workspaces = refreshed
	}
	*tasks = taskIndex()
	state.selectedName = name
	protected, warn := protectionSummary(output.String())
	message := "Created " + label
	if protected != "" {
		message += " · read-only for the agent: " + protected
	}
	role := roleOK
	if warn {
		role = roleWarn
		message += " · see agw doctor"
	}
	if action.agent == "" {
		if action.kind == createTask {
			message += " · c claude, x codex, f fetch"
		} else {
			message += " · c claude, x codex, enter shell"
		}
		setMessage(message, role)
		return nil, nil
	}
	setMessage(message, role)
	agent := action.agent
	return func() error {
		fmt.Fprintf(os.Stdout, "agw: %s in %s; exit it to return to the manager.\n", agent, terminalText(label))
		return service.RunAgent(name, []string{agent}, os.Stdout, os.Stderr)
	}, nil
}

func withoutWorkspace(workspaces []Workspace, name string) []Workspace {
	kept := make([]Workspace, 0, len(workspaces))
	for _, item := range workspaces {
		if item.Name != name {
			kept = append(kept, item)
		}
	}
	return kept
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return lines[len(lines)-1]
}

func chooseWorkspaceByNumber(service *WorkspaceService, in io.Reader, out, errOut io.Writer) error {
	workspaces, err := service.List()
	if err != nil {
		return err
	}
	if len(workspaces) == 0 {
		fmt.Fprintln(out, "No agentwisp workspaces found.")
		return nil
	}

	printWorkspaces(out, workspaces)
	fmt.Fprint(out, "\nInteractive TUI requires a terminal.")
	return nil
}

// renderTUI renders a frame without agent, task or proxy details. Tests and
// non-loop callers use it; the manager loop builds a full tuiView.
func renderTUI(out io.Writer, workspaces []Workspace, selected int, message string, width int) {
	rows := buildTUIRows(workspaces, nil, nil)
	if selected >= 0 && selected < len(workspaces) {
		selected = rowIndex(rows, workspaces[selected].Name)
	}
	var frame strings.Builder
	renderTUIView(&frame, tuiView{rows: rows, selected: selected, message: message, width: width, now: time.Now(), style: managerStyle(nil)})
	fmt.Fprint(out, frame.String())
}

func printWorkspaces(out io.Writer, workspaces []Workspace) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "#\tSTATUS\tCONTAINER\tWORKSPACE")
	for i, item := range workspaces {
		status := "stopped"
		if item.Running {
			status = "running"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", i+1, status, terminalText(item.Name), terminalText(item.Path))
	}
	_ = w.Flush()
}

func fitCell(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = truncateCell(value, width)
	runes := []rune(value)
	return value + strings.Repeat(" ", width-len(runes))
}

func truncateCell(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = terminalText(value)
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

// External paths, labels and errors must not control the terminal. Escape
// non-ASCII too: this keeps CJK/combining/emoji paths within cell boundaries
// without adding a terminal-width dependency. Actual paths stay unchanged.
func terminalText(value string) string {
	var out strings.Builder
	for _, r := range value {
		if r >= 32 && r <= 126 {
			out.WriteRune(r)
		} else {
			fmt.Fprintf(&out, "\\u{%x}", r)
		}
	}
	return out.String()
}

func renderDeleteConfirmation(out io.Writer, chosen tuiRow, width int, st tuiStyle) {
	width = max(1, width-1)
	fmt.Fprint(out, "\x1b[H\x1b[2J")
	lines := [][]span{
		{{" Delete ", roleError}, {chosen.label, roleStrong}, {"?", roleError}},
		nil,
		{{"   lost  ", roleError}, {"container files, installed tools, running agents", roleText}},
		{{"   kept  ", roleOK}, {"host project, traffic logs, saved agent logins", roleText}},
	}
	if chosen.task != nil {
		lines = append(lines, []span{{"   kept  ", roleOK}, {"task clone; ", roleText}, {"agw task rm " + chosen.task.Name, roleAccent}, {" removes it after fetch", roleText}})
	}
	lines = append(lines, nil,
		[]span{{"   container  ", roleDim}, {chosen.ws.Name, roleText}},
		[]span{{"   path       ", roleDim}, {homeRelative(chosen.ws.Path), roleText}},
		nil)
	for _, line := range lines {
		fmt.Fprintln(out, st.line(line, width))
	}
	if st.plain() {
		fmt.Fprintln(out, st.line([]span{{" [y] delete   any other key: cancel", roleText}}, width))
		return
	}
	fmt.Fprintln(out, st.line([]span{{" ", roleText}}, width)+st.paintWith(roleError, nil, true, "y")+" "+
		st.role(roleDim, "delete")+"   "+st.role(roleDim, "any other key: cancel"))
}
