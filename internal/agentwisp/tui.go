package agentwisp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
)

func chooseWorkspace(service *WorkspaceService, in io.Reader, out, errOut io.Writer) error {
	if in != os.Stdin || out != os.Stdout {
		return chooseWorkspaceByNumber(service, in, out, errOut)
	}

	term, err := enterRawTerminal()
	if err != nil {
		return chooseWorkspaceByNumber(service, in, out, errOut)
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
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM, syscall.SIGWINCH)
	defer signal.Stop(interrupts)

	reader := bufio.NewReader(in)
	workspaces, err := service.List()
	if err != nil {
		return err
	}

	selected := 0
	message := ""
	width := terminalWidth()
	keyCh := make(chan tuiKey, 1)
	errCh := make(chan error, 1)
	readingKey := false
	var pendingDelete *Workspace

	for {
		if pendingDelete != nil {
			renderDeleteConfirmation(out, *pendingDelete, width)
		} else {
			renderTUIWithMonitorStatus(out, workspaces, selected, message, width, monitorTUIStatus(workspaces, selected))
		}
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
			if pendingDelete != nil {
				chosen := *pendingDelete
				pendingDelete = nil
				if key != keyConfirm {
					message = "Removal cancelled"
					continue
				}
				if err := service.Remove(chosen.Name, true); err != nil {
					message = "Removal failed: " + err.Error()
					continue
				}
				// Remove locally first so refresh failure cannot leave a deleted row selected.
				workspaces = append(workspaces[:selected], workspaces[selected+1:]...)
				selected = min(selected, max(0, len(workspaces)-1))
				message = "Removed " + chosen.Name + "; host files and logs kept"
				continue
			}
			if len(workspaces) == 0 && key != keyRefresh && key != keyQuit {
				continue
			}
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
			case keyDelete:
				chosen := workspaces[selected]
				pendingDelete = &chosen
			case keyRefresh:
				selectedName := ""
				if len(workspaces) > 0 {
					selectedName = workspaces[selected].Name
				}
				refreshed, refreshErr := service.List()
				if refreshErr != nil {
					message = "Refresh failed: " + refreshErr.Error()
					continue
				}
				workspaces = refreshed
				selected = workspaceIndex(workspaces, selectedName)
				message = "Statuses refreshed"
			case keyStop:
				chosen := workspaces[selected]
				if !chosen.Running {
					message = chosen.Name + " is already stopped"
					continue
				}

				renderTUIWithMonitorStatus(out, workspaces, selected, "Stopping "+chosen.Name+"...", width, monitorTUIStatus(workspaces, selected))
				stopErr := service.Stop(chosen.Name)
				message = chosen.Name + " stopped; proxy stopped"
				if stopErr != nil {
					message = "Stop failed: " + stopErr.Error()
				}

				refreshed, refreshErr := service.List()
				if refreshErr != nil {
					message += "; refresh failed: " + refreshErr.Error()
					continue
				}
				workspaces = refreshed
				selected = workspaceIndex(workspaces, chosen.Name)
			case keyEnter:
				chosen := workspaces[selected]
				term.restore()
				fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
				screenOpen = false
				return service.Enter(chosen.Name, out, errOut)
			case keyQuit:
				return nil
			}
		}
	}
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

func renderTUI(out io.Writer, workspaces []Workspace, selected int, message string, width int) {
	renderTUIWithMonitorStatus(out, workspaces, selected, message, width, "")
}

func renderTUIWithMonitorStatus(out io.Writer, workspaces []Workspace, selected int, message string, width int, monitorStatus string) {
	if width < 2 {
		width = 2
	}
	// Keep the last terminal column empty. Writing into it can trigger an
	// automatic wrap before the following newline in many terminal emulators.
	contentWidth := width - 1
	panelWidth := adaptivePanelWidth(workspaces, contentWidth)

	var screen strings.Builder
	screen.Grow(panelWidth * (len(workspaces) + 8))
	screen.WriteString("\x1b[H\x1b[2J")
	renderTitleBar(&screen, workspaces, panelWidth)

	if len(workspaces) == 0 {
		screen.WriteString(truncateCell("No workspaces. R refresh, Q quit.", panelWidth))
		screen.WriteString("\n")
	} else {
		renderWorkspaceList(&screen, workspaces, selected, panelWidth)
	}

	if monitorStatus != "" {
		screen.WriteString("\n\x1b[38;5;250m")
		screen.WriteString(truncateCell(monitorStatus, panelWidth))
		screen.WriteString("\x1b[0m")
	}
	if message != "" {
		screen.WriteString("\n\x1b[33m")
		screen.WriteString(truncateCell(message, panelWidth))
		screen.WriteString("\x1b[0m")
	}
	screen.WriteString("\n")
	renderKeyBar(&screen, panelWidth)
	fmt.Fprint(out, screen.String())
}

func adaptivePanelWidth(workspaces []Workspace, terminalWidth int) int {
	// The workspace manager is a full-screen TUI. Use all available columns
	// instead of sizing the panel only to its current content; the VOLUME
	// column receives the extra room on wide terminals.
	if terminalWidth < 1 {
		return 1
	}
	return terminalWidth
}

func renderTitleBar(screen *strings.Builder, workspaces []Workspace, width int) {
	running := 0
	for _, item := range workspaces {
		if item.Running {
			running++
		}
	}
	stopped := len(workspaces) - running

	title := " AGENTWISP  WORKSPACES"
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

func renderWorkspaceList(screen *strings.Builder, workspaces []Workspace, selected, width int) {
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

func renderNarrowWorkspaceList(screen *strings.Builder, workspaces []Workspace, selected, width int) {
	const (
		statusWidth = 8
		gapWidth    = 4
	)
	if width < statusWidth+gapWidth+1 {
		for i, item := range workspaces {
			prefix := "  "
			if i == selected {
				prefix = "> "
			}
			screen.WriteString(truncateCell(prefix+item.Name, width) + "\n")
		}
		return
	}
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

func desiredNameWidth(workspaces []Workspace) int {
	width := len([]rune("WORKSPACE"))
	for _, item := range workspaces {
		if itemWidth := len([]rune(item.Name)); itemWidth > width {
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
		{key: "D", label: "Delete"},
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

func renderDeleteConfirmation(out io.Writer, chosen Workspace, width int) {
	width = max(1, width-1)
	fmt.Fprint(out, "\x1b[H\x1b[2J")
	for _, line := range []string{
		"DELETE WORKSPACE",
		chosen.Name,
		chosen.Path,
		"Stops running container and proxy.",
		"Container files/credentials: LOST.",
		"Host project and traffic logs: KEPT.",
		"Y: delete; any other key: cancel",
	} {
		fmt.Fprintln(out, truncateCell(line, width))
	}
}
