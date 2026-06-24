package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
)

const (
	containerPrefix = "codex-work-"
	workspaceLabel  = "dev.codexbox.workspace"
	managedLabel    = "dev.codexbox.managed"
)

type workspace struct {
	Name    string
	Path    string
	Running bool
}

type terminal struct {
	state string
}

type tuiKey int

const (
	keyUnknown tuiKey = iota
	keyUp
	keyDown
	keyEnter
	keyRefresh
	keyStop
	keyQuit
)

func main() {
	if err := execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "codexbox:", err)
		os.Exit(1)
	}
}

func execute(args []string, in io.Reader, out, errOut io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	project := sanitize(filepath.Base(cwd))
	if project == "" {
		project = "workspace"
	}
	name := containerPrefix + project

	command := "list"
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "add", "start", "run":
		return openWorkspace(name, cwd, out, errOut)
	case "list", "ls", ".":
		return chooseWorkspace(in, out, errOut)
	case "shell":
		return execInteractive(errOut, "container", "exec", "-it", name, "bash")
	case "stop":
		return execInteractive(errOut, "container", "stop", name)
	case "rm":
		return execInteractive(errOut, "container", "rm", name)
	case "help", "-h", "--help":
		printUsage(out)
		return nil
	default:
		printUsage(out)
		return fmt.Errorf("unknown command %q", command)
	}
}

func openWorkspace(name, cwd string, out, errOut io.Writer) error {
	exists, err := containerExists(name)
	if err != nil {
		return err
	}

	if !exists {
		return execInteractive(errOut, "container", "run", "-it",
			"--name", name,
			"--label", managedLabel+"=true",
			"--label", workspaceLabel+"="+cwd,
			"--volume", cwd+":/workspace",
			"--workdir", "/workspace",
			"docker.io/library/node:22-bookworm",
			"bash",
		)
	}

	running, err := containerRunning(name)
	if err != nil {
		return err
	}
	if !running {
		fmt.Fprintf(out, "Starting %s...\n", name)
		if err := execInteractive(errOut, "container", "start", name); err != nil {
			return err
		}
	}
	return execInteractive(errOut, "container", "exec", "-it", name, "bash")
}

func chooseWorkspace(in io.Reader, out, errOut io.Writer) error {
	if in != os.Stdin || out != os.Stdout {
		return chooseWorkspaceByNumber(in, out, errOut)
	}

	term, err := enterRawTerminal()
	if err != nil {
		return chooseWorkspaceByNumber(in, out, errOut)
	}
	defer term.restore()

	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
	screenOpen := true
	defer func() {
		if screenOpen {
			fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
		}
	}()

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGWINCH)
	defer signal.Stop(interrupts)

	reader := bufio.NewReader(in)
	workspaces, err := listWorkspaces()
	if err != nil {
		return err
	}
	if len(workspaces) == 0 {
		renderEmptyTUI(out)
		_, _ = readTUIKey(reader)
		return nil
	}

	selected := 0
	message := ""
	width := terminalWidth()
	keyCh := make(chan tuiKey, 1)
	errCh := make(chan error, 1)
	readingKey := false

	for {
		renderTUI(out, workspaces, selected, message, width)
		message = ""

		if !readingKey {
			readingKey = true
			go func() {
				key, readErr := readTUIKey(reader)
				if readErr != nil {
					errCh <- readErr
					return
				}
				keyCh <- key
			}()
		}

		select {
		case received := <-interrupts:
			if received == syscall.SIGWINCH {
				width = terminalWidth()
				continue
			}
			return nil
		case readErr := <-errCh:
			readingKey = false
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		case key := <-keyCh:
			readingKey = false
			switch key {
			case keyUp:
				selected--
				if selected < 0 {
					selected = len(workspaces) - 1
				}
			case keyDown:
				selected++
				if selected >= len(workspaces) {
					selected = 0
				}
			case keyRefresh:
				refreshed, refreshErr := listWorkspaces()
				if refreshErr != nil {
					message = "Refresh failed: " + refreshErr.Error()
					continue
				}
				workspaces = refreshed
				if len(workspaces) == 0 {
					renderEmptyTUI(out)
					_, _ = readTUIKey(reader)
					return nil
				}
				if selected >= len(workspaces) {
					selected = len(workspaces) - 1
				}
				message = "Statuses refreshed"
			case keyStop:
				chosen := workspaces[selected]
				if !chosen.Running {
					message = chosen.Name + " is already stopped"
					continue
				}

				renderTUI(out, workspaces, selected, "Stopping "+chosen.Name+"...", width)
				if stopErr := stopWorkspace(chosen.Name); stopErr != nil {
					message = "Stop failed: " + stopErr.Error()
					continue
				}

				refreshed, refreshErr := listWorkspaces()
				if refreshErr != nil {
					message = "Stopped, but refresh failed: " + refreshErr.Error()
					continue
				}
				workspaces = refreshed
				selected = workspaceIndex(workspaces, chosen.Name)
				message = chosen.Name + " stopped"
			case keyEnter:
				chosen := workspaces[selected]
				term.restore()
				fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
				screenOpen = false
				return enterWorkspace(chosen, out, errOut)
			case keyQuit:
				return nil
			}
		}
	}
}

func chooseWorkspaceByNumber(in io.Reader, out, errOut io.Writer) error {
	workspaces, err := listWorkspaces()
	if err != nil {
		return err
	}
	if len(workspaces) == 0 {
		fmt.Fprintln(out, "No codexbox workspaces found.")
		return nil
	}

	printWorkspaces(out, workspaces)
	fmt.Fprint(out, "\nInteractive TUI requires a terminal.")
	return nil
}

func enterWorkspace(selected workspace, out, errOut io.Writer) error {
	if !selected.Running {
		fmt.Fprintf(out, "Starting %s...\n", selected.Name)
		if err := execInteractive(errOut, "container", "start", selected.Name); err != nil {
			return err
		}
	}
	return execInteractive(errOut, "container", "exec", "-it", selected.Name, "bash")
}

func enterRawTerminal() (*terminal, error) {
	stateCmd := exec.Command("stty", "-g")
	stateCmd.Stdin = os.Stdin
	state, err := stateCmd.Output()
	if err != nil {
		return nil, err
	}

	// Do not use `stty raw` here. Raw mode disables output processing
	// (OPOST/ONLCR), so '\n' no longer returns the cursor to column zero and
	// every rendered row starts farther to the right. We only need immediate,
	// non-echoed keyboard input; leave terminal output processing intact.
	inputCmd := exec.Command("stty", "-icanon", "-echo", "min", "1", "time", "0")
	inputCmd.Stdin = os.Stdin
	if err := inputCmd.Run(); err != nil {
		return nil, err
	}
	return &terminal{state: strings.TrimSpace(string(state))}, nil
}

func (t *terminal) restore() {
	if t == nil || t.state == "" {
		return
	}
	cmd := exec.Command("stty", t.state)
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
	t.state = ""
}

func readTUIKey(reader *bufio.Reader) (tuiKey, error) {
	first, err := reader.ReadByte()
	if err != nil {
		return keyUnknown, err
	}

	switch first {
	case '\r', '\n':
		return keyEnter, nil
	case 'k', 'K':
		return keyUp, nil
	case 'j', 'J':
		return keyDown, nil
	case 'r', 'R':
		return keyRefresh, nil
	case 's', 'S':
		return keyStop, nil
	case 'q', 'Q', 3:
		return keyQuit, nil
	case 27:
		if reader.Buffered() == 0 {
			return keyQuit, nil
		}
		second, err := reader.ReadByte()
		if err != nil {
			return keyQuit, nil
		}
		if second != '[' {
			return keyQuit, nil
		}
		third, err := reader.ReadByte()
		if err != nil {
			return keyUnknown, nil
		}
		switch third {
		case 'A':
			return keyUp, nil
		case 'B':
			return keyDown, nil
		}
	}
	return keyUnknown, nil
}

func renderTUI(out io.Writer, workspaces []workspace, selected int, message string, width int) {
	if width < 20 {
		width = 20
	}
	// Keep the last terminal column empty. Writing into it can trigger an
	// automatic wrap before the following newline in many terminal emulators.
	contentWidth := width - 1
	panelWidth := adaptivePanelWidth(workspaces, contentWidth)

	var screen strings.Builder
	screen.Grow(panelWidth * (len(workspaces) + 8))
	screen.WriteString("\x1b[H\x1b[2J")
	renderTitleBar(&screen, workspaces, panelWidth)

	renderWorkspaceList(&screen, workspaces, selected, panelWidth)

	if message != "" {
		screen.WriteString("\n\x1b[33m")
		screen.WriteString(truncateCell(message, panelWidth))
		screen.WriteString("\x1b[0m")
	}
	screen.WriteString("\n")
	renderKeyBar(&screen, panelWidth)
	fmt.Fprint(out, screen.String())
}

func adaptivePanelWidth(workspaces []workspace, terminalWidth int) int {
	// The workspace manager is a full-screen TUI. Use all available columns
	// instead of sizing the panel only to its current content; the VOLUME
	// column receives the extra room on wide terminals.
	if terminalWidth < 20 {
		return 20
	}
	return terminalWidth
}

func renderTitleBar(screen *strings.Builder, workspaces []workspace, width int) {
	running := 0
	for _, item := range workspaces {
		if item.Running {
			running++
		}
	}
	stopped := len(workspaces) - running

	title := " CODEXBOX  WORKSPACES"
	summary := fmt.Sprintf("%d total  %d active  %d stopped ", len(workspaces), running, stopped)
	space := width - len([]rune(title)) - len([]rune(summary))
	if space < 1 {
		summary = fmt.Sprintf("%d/%d ", running, len(workspaces))
		space = width - len([]rune(title)) - len([]rune(summary))
	}
	if space < 0 {
		space = 0
	}

	screen.WriteString("\x1b[1;37;44m")
	screen.WriteString(truncateCell(title+strings.Repeat(" ", space)+summary, width))
	screen.WriteString("\x1b[0m\n")
	screen.WriteString("\x1b[38;5;244m")
	screen.WriteString(truncateCell(" Choose a workspace to start or attach", width))
	screen.WriteString("\x1b[0m\n")
	screen.WriteString("\x1b[38;5;240m")
	screen.WriteString(strings.Repeat("─", width))
	screen.WriteString("\x1b[0m\n")
}

func renderWorkspaceList(screen *strings.Builder, workspaces []workspace, selected, width int) {
	const (
		statusWidth = 8
		actionWidth = 8
		gapWidth    = 4
		minName     = 12
		minVolume   = 16
	)

	showVolume := width >= minName+statusWidth+actionWidth+minVolume+gapWidth*3
	if !showVolume {
		renderNarrowWorkspaceList(screen, workspaces, selected, width)
		return
	}

	available := width - statusWidth - actionWidth - gapWidth*3
	nameWidth := clamp(desiredNameWidth(workspaces), minName, 36)
	if nameWidth > available-minVolume {
		nameWidth = available - minVolume
	}
	volumeWidth := available - nameWidth
	gap := strings.Repeat(" ", gapWidth)

	fmt.Fprintf(screen, "\x1b[1;30;46m%s%s%s%s%s%s%s\x1b[0m\n",
		fitCell("WORKSPACE", nameWidth),
		gap,
		fitCell("STATUS", statusWidth),
		gap,
		fitCell("ACTION", actionWidth),
		gap,
		fitCell("VOLUME", volumeWidth),
	)

	for i, item := range workspaces {
		name := item.Name
		if i == selected && nameWidth >= 3 {
			name = "> " + name
		}
		row := fmt.Sprintf("%s%s%s%s%s%s%s",
			fitCell(name, nameWidth),
			gap,
			fitCell(statusText(item.Running), statusWidth),
			gap,
			fitCell(actionText(item.Running), actionWidth),
			gap,
			fitCell(item.Path, volumeWidth),
		)
		if i == selected {
			fmt.Fprintf(screen, "\x1b[1;30;48;5;153m%s\x1b[0m\n", row)
		} else {
			fmt.Fprintf(screen, "%s%s%s%s%s%s%s\n",
				fitCell(name, nameWidth),
				gap,
				coloredStatus(item.Running, statusWidth),
				gap,
				"\x1b[38;5;110m"+fitCell(actionText(item.Running), actionWidth)+"\x1b[0m",
				gap,
				"\x1b[38;5;245m"+fitCell(item.Path, volumeWidth)+"\x1b[0m",
			)
		}
	}
}

func renderNarrowWorkspaceList(screen *strings.Builder, workspaces []workspace, selected, width int) {
	const (
		statusWidth = 8
		gapWidth    = 4
	)
	nameWidth := width - statusWidth - gapWidth
	gap := strings.Repeat(" ", gapWidth)

	fmt.Fprintf(screen, "\x1b[1;30;46m%s%s%s\x1b[0m\n",
		fitCell("WORKSPACE", nameWidth),
		gap,
		fitCell("STATUS", statusWidth),
	)
	for i, item := range workspaces {
		name := item.Name
		if i == selected && nameWidth >= 3 {
			name = "> " + name
		}
		row := fmt.Sprintf("%s%s%s",
			fitCell(name, nameWidth),
			gap,
			fitCell(statusText(item.Running), statusWidth),
		)
		if i == selected {
			fmt.Fprintf(screen, "\x1b[1;30;48;5;153m%s\x1b[0m\n", row)
		} else {
			fmt.Fprintf(screen, "%s%s%s\n",
				fitCell(name, nameWidth),
				gap,
				coloredStatus(item.Running, statusWidth),
			)
		}
	}
	if selected >= 0 && selected < len(workspaces) {
		screen.WriteString("\n\x1b[2mVOLUME\x1b[0m\n")
		screen.WriteString(truncateCell(workspaces[selected].Path, width))
	}
}

func desiredNameWidth(workspaces []workspace) int {
	width := len([]rune("WORKSPACE"))
	for _, item := range workspaces {
		if itemWidth := len([]rune(item.Name)); itemWidth > width {
			width = itemWidth
		}
	}
	return width
}

func desiredVolumeWidth(workspaces []workspace) int {
	width := len([]rune("VOLUME"))
	for _, item := range workspaces {
		if itemWidth := len([]rune(item.Path)); itemWidth > width {
			width = itemWidth
		}
	}
	return width
}

func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func coloredStatus(running bool, width int) string {
	text := statusText(running)
	color := "\x1b[1;38;5;167m"
	if running {
		color = "\x1b[1;38;5;78m"
	}
	return color + fitCell(text, width) + "\x1b[0m"
}

func statusText(running bool) string {
	if running {
		return "RUNNING"
	}
	return "STOPPED"
}

func actionText(running bool) string {
	if running {
		return "ATTACH"
	}
	return "START"
}

func renderKeyBar(screen *strings.Builder, width int) {
	type keyHint struct {
		key   string
		label string
	}
	hints := []keyHint{
		{key: "J/K", label: "Move"},
		{key: "Enter", label: "Open"},
		{key: "S", label: "Stop"},
		{key: "R", label: "Refresh"},
		{key: "Q", label: "Quit"},
	}

	used := 0
	screen.WriteString("\x1b[38;5;240m")
	screen.WriteString(strings.Repeat("─", width))
	screen.WriteString("\x1b[0m\n")
	for _, hint := range hints {
		plainWidth := len([]rune(hint.key)) + len([]rune(hint.label)) + 4
		if used+plainWidth > width {
			break
		}
		fmt.Fprintf(screen, "\x1b[1;30;48;5;117m %s \x1b[0m \x1b[38;5;250m%s\x1b[0m ", hint.key, hint.label)
		used += plainWidth
	}
}

func stopWorkspace(name string) error {
	_, err := commandOutput("container", "stop", name)
	return err
}

func workspaceIndex(workspaces []workspace, name string) int {
	for i, item := range workspaces {
		if item.Name == name {
			return i
		}
	}
	if len(workspaces) == 0 {
		return 0
	}
	return len(workspaces) - 1
}

func terminalWidth() int {
	cmd := exec.Command("stty", "size")
	cmd.Stdin = os.Stdin
	output, err := cmd.Output()
	if err == nil {
		fields := strings.Fields(string(output))
		if len(fields) == 2 {
			if width, parseErr := strconv.Atoi(fields[1]); parseErr == nil && width > 0 {
				return width
			}
		}
	}
	if width, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && width > 0 {
		return width
	}
	return 80
}

func fitCell(value string, width int) string {
	value = truncateCell(value, width)
	runes := []rune(value)
	return value + strings.Repeat(" ", width-len(runes))
}

func truncateCell(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func renderEmptyTUI(out io.Writer) {
	fmt.Fprint(out,
		"\x1b[H\x1b[2J",
		"\x1b[1;36mCODEXBOX WORKSPACES\x1b[0m\n\n",
		"No codexbox workspaces found.\n\n",
		"\x1b[2mPress any key to close\x1b[0m",
	)
}

func listWorkspaces() ([]workspace, error) {
	all, err := containerIDs(true)
	if err != nil {
		return nil, err
	}
	runningIDs, err := containerIDs(false)
	if err != nil {
		return nil, err
	}

	running := make(map[string]bool, len(runningIDs))
	for _, id := range runningIDs {
		running[id] = true
	}

	workspaces := make([]workspace, 0, len(all))
	for _, name := range all {
		if !strings.HasPrefix(name, containerPrefix) {
			continue
		}

		path, err := inspectWorkspacePath(name)
		if err != nil {
			return nil, err
		}
		if path == "" {
			path = "-"
		}
		workspaces = append(workspaces, workspace{
			Name:    name,
			Path:    path,
			Running: running[name],
		})
	}

	sort.Slice(workspaces, func(i, j int) bool {
		if workspaces[i].Running != workspaces[j].Running {
			return workspaces[i].Running
		}
		return workspaces[i].Name < workspaces[j].Name
	})
	return workspaces, nil
}

func printWorkspaces(out io.Writer, workspaces []workspace) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "#\tSTATUS\tCONTAINER\tWORKSPACE")
	for i, item := range workspaces {
		status := "stopped"
		if item.Running {
			status = "running"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", i+1, status, item.Name, item.Path)
	}
	_ = w.Flush()
}

func containerIDs(all bool) ([]string, error) {
	args := []string{"list"}
	if all {
		args = append(args, "--all")
	}
	args = append(args, "--quiet")

	output, err := commandOutput("container", args...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(output)), nil
}

func inspectWorkspacePath(name string) (string, error) {
	output, err := commandOutput("container", "inspect", name)
	if err != nil {
		return "", err
	}
	return workspacePathFromInspect(output)
}

func workspacePathFromInspect(data []byte) (string, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return "", fmt.Errorf("parse container inspect output: %w", err)
	}

	if path := findLabel(value, workspaceLabel); path != "" {
		return path, nil
	}
	return findWorkspaceMount(value), nil
}

func findLabel(value any, label string) string {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if strings.EqualFold(key, label) {
				if text, ok := child.(string); ok {
					return text
				}
			}
			if strings.EqualFold(key, "labels") {
				if labels, ok := child.(map[string]any); ok {
					if text, ok := labels[label].(string); ok {
						return text
					}
				}
			}
			if found := findLabel(child, label); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range current {
			if found := findLabel(child, label); found != "" {
				return found
			}
		}
	}
	return ""
}

func findWorkspaceMount(value any) string {
	switch current := value.(type) {
	case map[string]any:
		target := mapString(current, "target", "destination", "dst")
		source := mapString(current, "source", "src")
		if target == "/workspace" && source != "" {
			return source
		}
		for _, child := range current {
			if found := findWorkspaceMount(child); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range current {
			if found := findWorkspaceMount(child); found != "" {
				return found
			}
		}
	case string:
		if source, ok := strings.CutSuffix(current, ":/workspace"); ok {
			return source
		}
	}
	return ""
}

func mapString(values map[string]any, keys ...string) string {
	for key, value := range values {
		for _, wanted := range keys {
			if strings.EqualFold(key, wanted) {
				if text, ok := value.(string); ok {
					return text
				}
			}
		}
	}
	return ""
}

func sanitize(s string) string {
	s = strings.ToLower(s)
	re := regexp.MustCompile(`[^a-z0-9-]+`)
	s = re.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func containerExists(name string) (bool, error) {
	cmd := exec.Command("container", "inspect", name)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return false, fmt.Errorf("container CLI not found")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, err
}

func containerRunning(name string) (bool, error) {
	ids, err := containerIDs(false)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if id == name {
			return true, nil
		}
	}
	return false, nil
}

func commandOutput(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.Output()
	if err == nil {
		return output, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil, fmt.Errorf("%s CLI not found", name)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		message := strings.TrimSpace(string(exitErr.Stderr))
		if message != "" {
			return nil, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), message)
		}
	}
	return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
}

func execInteractive(errOut io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = errOut
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%s CLI not found", name)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, "codexbox - manage development workspaces")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  codexbox              Open the workspace manager")
	fmt.Fprintln(out, "  codexbox add          Add/start the current directory and enter it")
	fmt.Fprintln(out, "  codexbox start        Start the current directory and enter it")
	fmt.Fprintln(out, "  codexbox shell        Enter the current directory workspace")
	fmt.Fprintln(out, "  codexbox stop         Stop the current directory workspace")
	fmt.Fprintln(out, "  codexbox rm           Remove the current directory workspace")
	fmt.Fprintln(out, "  codexbox help         Show this help")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Aliases:")
	fmt.Fprintln(out, "  run = start, list/ls/. = workspace manager")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Workspace manager keys:")
	fmt.Fprintln(out, "  Up/Down or J/K        Move selection")
	fmt.Fprintln(out, "  Enter                 Attach to running or start stopped workspace")
	fmt.Fprintln(out, "  S                     Stop selected workspace")
	fmt.Fprintln(out, "  R                     Refresh workspace statuses")
	fmt.Fprintln(out, "  Q or Esc              Quit")
}
