package agentwisp

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The manager view is pure: rows and details are prepared by the loop (which
// reads state files and probes the proxy), rendering only formats them. All
// text that reaches the terminal passes through fitCell/truncate*, which
// escape untrusted characters and keep every line within the content width.

type tuiRow struct {
	ws    Workspace
	label string      // Human name: project, or "repo / task".
	task  *taskRecord // Non-nil for agw task clones.
	agent agentInfo
}

type tuiView struct {
	rows        []tuiRow
	selected    int
	offset      int // First visible list line when the list scrolls.
	message     string
	messageRole tuiRole
	width       int
	height      int        // 0 means unknown: show everything.
	detail      *tuiDetail // Facts about the selected row, gathered by the loop.
	help        bool
	create      *createState // The new-workspace screen, when open.
	now         time.Time
	style       tuiStyle
}

var hashSuffix = regexp.MustCompile(`-[0-9a-f]{16}$`)

// buildTUIRows derives display rows and orders them by urgency so the
// workspace that needs the user is at the top.
func buildTUIRows(workspaces []Workspace, agents map[string]agentInfo, tasks map[string]taskRecord) []tuiRow {
	rows := make([]tuiRow, 0, len(workspaces))
	for _, item := range workspaces {
		row := tuiRow{ws: item, agent: agents[item.Name]}
		if task, ok := tasks[item.Path]; ok {
			row.task = &task
			row.label = filepath.Base(task.Repo) + " / " + task.Name
		} else if filepath.IsAbs(item.Path) {
			row.label = filepath.Base(item.Path)
		} else {
			label := strings.TrimPrefix(strings.TrimPrefix(item.Name, containerPrefix), legacyContainerPrefix)
			row.label = hashSuffix.ReplaceAllString(label, "")
		}
		rows = append(rows, row)
	}
	// Same basename in different directories: add the parent directory.
	counts := map[string]int{}
	for _, row := range rows {
		counts[row.label]++
	}
	for i, row := range rows {
		if counts[row.label] > 1 && row.task == nil && filepath.IsAbs(row.ws.Path) {
			rows[i].label = filepath.Base(filepath.Dir(row.ws.Path)) + "/" + row.label
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if a, b := rowUrgency(rows[i]), rowUrgency(rows[j]); a != b {
			return a < b
		}
		return rows[i].label < rows[j].label
	})
	return rows
}

// Sections follow where the user's attention is needed (as in agent
// dashboards such as Codex's "Need input / Working / Ready"): rows move only
// when their state changes, and stay alphabetical inside a section.
type tuiSection struct {
	title string
	role  tuiRole
}

var tuiSections = []tuiSection{
	{"NEEDS YOU", roleUrgent},
	{"READY", roleIdle},
	{"WORKING", roleWorking},
	{"NO AGENT", roleDim},
	{"STOPPED", roleFaint},
}

func rowUrgency(row tuiRow) int {
	if !row.ws.Running {
		return 4
	}
	switch row.agent.State {
	case "needs-input":
		return 0
	case "idle":
		return 1
	case "working", "starting":
		return 2
	}
	return 3
}

func rowIndex(rows []tuiRow, name string) int {
	for i, row := range rows {
		if row.ws.Name == name {
			return i
		}
	}
	return 0
}

func shortAge(since, now time.Time) string {
	if since.IsZero() {
		return ""
	}
	d := now.Sub(since)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// agentMeta is the right side of a row: which agent and for how long, or how
// many sessions wait when several do.
func agentMeta(row tuiRow, now time.Time) string {
	if !row.ws.Running || row.agent.State == "" {
		return ""
	}
	age := shortAge(row.agent.Since, now)
	if row.agent.Waiting > 1 {
		return strings.TrimSpace(fmt.Sprintf("%d waiting %s", row.agent.Waiting, age))
	}
	provider := ""
	for _, s := range row.agent.Sessions {
		if s.State == row.agent.State {
			provider = s.Provider
			break
		}
	}
	if len(row.agent.Sessions) > 1 {
		provider = fmt.Sprintf("%d agents", len(row.agent.Sessions))
	}
	return strings.TrimSpace(provider + " " + age)
}

func rowNote(row tuiRow) string {
	if max(row.ws.Layout, 1) < currentWorkspaceLayout {
		return "old"
	}
	return ""
}

func homeRelative(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && (path == home || strings.HasPrefix(path, home+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// truncateLeft keeps the end of a path, where the project name is.
func truncateLeft(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(terminalText(value))
	if len(runes) <= width {
		return string(runes)
	}
	if width == 1 {
		return "…"
	}
	return "…" + string(runes[len(runes)-width+1:])
}

func fitLeft(value string, width int) string {
	value = truncateLeft(value, width)
	return value + strings.Repeat(" ", max(0, width-len([]rune(value))))
}

// tuiDetail holds facts about the selected workspace that need file reads or
// a proxy probe; the loop gathers them, rendering stays pure.
type tuiDetail struct {
	proxy   []span
	logins  span
	files   span
	protect []span
	layout  []span
}

func tuiDetails(row tuiRow, now time.Time) *tuiDetail {
	d := &tuiDetail{proxy: []span{{"off", roleDim}}}
	if cfg, err := loadMonitorConfig(row.ws.Name); err == nil && cfg.Enabled {
		// Color only what needs attention; normal values stay plain text.
		scope := span{"all hosts, monitor only", roleText}
		if len(cfg.AllowHosts) > 0 {
			scope = span{fmt.Sprintf("allowlist %d host(s)", len(cfg.AllowHosts)), roleText}
		}
		health := span{"starts with next session", roleDim}
		if cfg.Port > 0 {
			health = span{"down", roleError}
			if monitorProxyHealthy(cfg) {
				health = span{"up", roleOK}
			}
		}
		d.proxy = []span{{"on", roleText}, {" · ", roleFaint}, scope, {" · ", roleFaint}, health}
	}
	d.logins = span{"kept in container only", roleWarn}
	if info, err := os.Stat(filepath.Join(monitorStateDir(row.ws.Name), "home")); err == nil && info.IsDir() && max(row.ws.Layout, 1) >= 2 {
		d.logins = span{"saved on host", roleText}
	}
	switch {
	case row.task != nil:
		d.files = span{"isolated clone, main .git not mounted", roleText}
	case filepath.IsAbs(row.ws.Path):
		d.files = span{"project mounted, writable", roleText}
		if _, err := os.Lstat(filepath.Join(row.ws.Path, ".git")); err == nil && !contains(row.ws.Readonly, ".git") {
			d.files = span{".git writable by agent, recreate or agw task", roleWarn}
		}
	default:
		d.files = span{"host path unknown", roleWarn}
	}
	switch {
	case max(row.ws.Layout, 1) < 3:
		d.protect = []span{{"none (older layout)", roleWarn}}
	case len(row.ws.Readonly) == 0:
		d.protect = []span{{"nothing found to protect", roleDim}}
	default:
		d.protect = []span{{strings.Join(row.ws.Readonly, " "), roleText}, {" · read-only for the agent", roleDim}}
	}
	d.layout = []span{{fmt.Sprint(max(row.ws.Layout, 1)), roleText}, {" · current", roleDim}}
	if rowNote(row) != "" {
		d.layout = []span{{fmt.Sprint(max(row.ws.Layout, 1)), roleWarn}, {" · old, recreate (agw doctor)", roleWarn}}
	}
	return d
}

func rowRole(row tuiRow) tuiRole { return tuiSections[rowUrgency(row)].role }

func sessionState(state string) (string, tuiRole) {
	switch state {
	case "needs-input":
		return "needs input", roleUrgent
	case "idle":
		return "ready", roleIdle
	case "starting":
		return "starting", roleWorking
	}
	return state, roleWorking
}

// listLine is one line of the workspace list: a section header or a row.
type listLine struct {
	section int
	row     int // -1 for a header.
}

// buildListLines lays out sections; spaced adds an empty line between them
// (used when the terminal has the height for it).
func buildListLines(rows []tuiRow, spaced bool) []listLine {
	var lines []listLine
	for i, row := range rows {
		section := rowUrgency(row)
		if i == 0 || rowUrgency(rows[i-1]) != section {
			if spaced && i > 0 {
				lines = append(lines, listLine{section: section, row: -2})
			}
			lines = append(lines, listLine{section: section, row: -1})
		}
		lines = append(lines, listLine{section: section, row: i})
	}
	return lines
}

func listLinesFor(view tuiView, l tuiLayout) []listLine {
	spaced := buildListLines(view.rows, true)
	if l.bodyHeight == 0 || len(spaced) <= l.bodyHeight {
		return spaced
	}
	return buildListLines(view.rows, false)
}

const twoPaneMinWidth = 100

type tuiLayout struct {
	content, listWidth, detailWidth int
	twoPane                         bool
	bodyHeight                      int // 0 = unlimited.
}

func layoutFor(view tuiView) tuiLayout {
	content := max(view.width, 2) - 1 // Keep the last terminal column empty.
	l := tuiLayout{content: content, listWidth: content}
	if content >= twoPaneMinWidth && (view.height == 0 || view.height >= 14) {
		l.twoPane = true
		l.listWidth = min(max(content*46/100, 44), 64)
		l.detailWidth = content - l.listWidth - 3
	}
	if view.height > 0 {
		fixed := 4 // header, rule, rule, hints
		if view.message != "" {
			fixed++
		}
		if !l.twoPane {
			fixed += 3 // rule + two compact detail lines
		}
		l.bodyHeight = max(1, view.height-fixed)
	}
	return l
}

// scrollOffset keeps the selected row (and, when possible, its section
// header) visible with minimal movement.
func scrollOffset(view tuiView) int {
	l := layoutFor(view)
	lines := listLinesFor(view, l)
	if l.bodyHeight == 0 || len(lines) <= l.bodyHeight {
		return 0
	}
	capacity := max(1, l.bodyHeight-2) // Room for the "more" indicators.
	target := 0
	for i, line := range lines {
		if line.row == view.selected {
			target = i
		}
	}
	top := target
	if top > 0 && lines[top-1].row == -1 {
		top-- // Show the section header with its first row.
	}
	offset := view.offset
	if top < offset {
		offset = top
	}
	if target >= offset+capacity {
		offset = target - capacity + 1
	}
	return min(max(0, offset), len(lines)-capacity)
}

func renderTUIView(out *strings.Builder, view tuiView) {
	st := view.style
	l := layoutFor(view)
	out.WriteString("\x1b[H\x1b[2J")
	if view.create != nil {
		out.WriteString(st.line([]span{{" agentwisp", roleBrand}, {" new workspace", roleMeta}}, l.content) + "\n")
	} else {
		out.WriteString(renderManagerHeader(view.rows, l.content, st) + "\n")
	}
	out.WriteString(st.rule(l.content) + "\n")

	if view.create != nil {
		body := renderCreate(view, l)
		if view.height > 0 {
			room := view.height - 4 // header, rule, rule, hints
			if view.message != "" {
				room--
			}
			body = body[:min(len(body), max(0, room))]
		}
		for _, line := range body {
			out.WriteString(line + "\n")
		}
		out.WriteString(st.rule(l.content) + "\n")
		if view.message != "" {
			out.WriteString(st.line([]span{{" " + view.message, view.messageRole}}, l.content) + "\n")
		}
		out.WriteString(renderHints(st, createKeyHints(view.create), nil, l.content))
		return
	}
	if view.help {
		for _, line := range tuiHelpLines(st) {
			out.WriteString(st.line(line, l.content) + "\n")
		}
		out.WriteString(st.rule(l.content) + "\n")
		out.WriteString(st.line([]span{{" press any key to return", roleDim}}, l.content) + "\n")
		return
	}

	left := renderList(view, l)
	if l.twoPane {
		right := renderDetailPane(view, l.detailWidth)
		height := max(len(left), len(right))
		if l.bodyHeight > 0 {
			height = l.bodyHeight
		}
		blank := strings.Repeat(" ", l.listWidth)
		for i := 0; i < height; i++ {
			lineLeft, lineRight := blank, ""
			if i < len(left) {
				lineLeft = left[i]
			}
			if i < len(right) {
				lineRight = right[i]
			}
			out.WriteString(lineLeft + " " + st.role(roleRule, st.g.vline) + " " + lineRight + "\n")
		}
	} else {
		for _, line := range left {
			out.WriteString(line + "\n")
		}
		out.WriteString(st.rule(l.content) + "\n")
		for _, line := range renderCompactDetail(view, l.content) {
			out.WriteString(line + "\n")
		}
	}

	out.WriteString(st.rule(l.content) + "\n")
	if view.message != "" {
		out.WriteString(st.line([]span{{" " + view.message, view.messageRole}}, l.content) + "\n")
	}
	out.WriteString(renderKeyHints(view, l.content))
}

func renderManagerHeader(rows []tuiRow, content int, st tuiStyle) string {
	counts := make([]int, len(tuiSections))
	for _, row := range rows {
		counts[rowUrgency(row)]++
	}
	labels := []string{"need you", "ready", "working", "no agent", "stopped"}
	const title = " agentwisp"
	// Right side: "● 1 need you   ● 2 working ..." while it fits; the most
	// urgent counts come first, so they are the last to be dropped.
	var parts []string
	plainWidth := 0
	for i, count := range counts {
		if count == 0 {
			continue
		}
		glyph := st.g.dot
		if i == 4 {
			glyph = st.g.ring
		}
		number := fmt.Sprint(count)
		width := 2 + len(number) + 1 + len(labels[i])
		if len(parts) > 0 {
			width += 3
		}
		if len(title)+2+plainWidth+width+1 > content {
			break
		}
		part := st.glyph(tuiSections[i].role, nil, st.displayText(glyph)) + " " + st.role(roleStrong, number) + " " + st.role(roleDim, labels[i])
		if len(parts) > 0 {
			part = "   " + part
		}
		parts = append(parts, part)
		plainWidth += width
	}
	left := st.line([]span{{title, roleBrand}}, content)
	if len(title)+len(" workspaces")+2+plainWidth+1 <= content {
		left = st.line([]span{{title, roleBrand}, {" workspaces", roleMeta}}, content)
		plainWidth += len(" workspaces")
	}
	if len(parts) == 0 {
		return left
	}
	gap := content - len(title) - plainWidth - 1
	return left + strings.Repeat(" ", gap) + strings.Join(parts, "")
}

// renderList returns the list pane: section headers and rows, each exactly
// l.listWidth cells, windowed to the body height.
func renderList(view tuiView, l tuiLayout) []string {
	st := view.style
	width := l.listWidth
	if len(view.rows) == 0 {
		return renderEmptyState(st, width)
	}
	lines := listLinesFor(view, l)
	first, last := 0, len(lines)
	if l.bodyHeight > 0 && len(lines) > l.bodyHeight {
		capacity := max(1, l.bodyHeight-2)
		first = min(view.offset, len(lines)-capacity)
		last = min(len(lines), first+capacity)
	}
	showPath := !l.twoPane && width >= 70
	hasNote := false
	for _, row := range view.rows {
		hasNote = hasNote || rowNote(row) != ""
	}
	var out []string
	if first > 0 {
		out = append(out, st.lineOn([]span{{fmt.Sprintf("   ^ %d more", first), roleFaint}}, width, nil, true))
	}
	for _, line := range lines[first:last] {
		if line.row == -2 {
			out = append(out, strings.Repeat(" ", width))
			continue
		}
		if line.row < 0 {
			section := tuiSections[line.section]
			count := 0
			for _, row := range view.rows {
				if rowUrgency(row) == line.section {
					count++
				}
			}
			// Section titles share one weight; color tells them apart.
			label := fmt.Sprintf("  %d", count)
			if 2+len(section.title)+len(label) > width {
				out = append(out, st.lineOn([]span{{"  " + section.title, section.role}}, width, nil, true))
				continue
			}
			out = append(out, "  "+st.paintWith(section.role, nil, true, section.title)+st.role(roleMeta, label)+
				strings.Repeat(" ", width-2-len(section.title)-len(label)))
			continue
		}
		out = append(out, renderListRow(view, view.rows[line.row], line.row == view.selected, width, showPath, hasNote))
	}
	if last < len(lines) {
		out = append(out, st.lineOn([]span{{fmt.Sprintf("   v %d more", len(lines)-last), roleFaint}}, width, nil, true))
	}
	return out
}

func renderListRow(view tuiView, row tuiRow, selected bool, width int, showPath, hasNote bool) string {
	st := view.style
	var bg *rgb
	if selected && !st.plain() {
		bg = &st.pal.selBG
	}
	meta := agentMeta(row, view.now)
	metaWidth := min(14, max(0, width/4))
	noteWidth := 0
	if hasNote {
		noteWidth = 5 // "  old"
	}
	pathWidth := 0
	if showPath {
		pathWidth = min(36, max(0, width-4-20-metaWidth-noteWidth-2))
	}
	labelWidth := max(1, width-4-metaWidth-noteWidth-pathWidth-1)
	if labelWidth+4+metaWidth+noteWidth+pathWidth+1 > width {
		// Tiny terminals: label and marker only.
		metaWidth, noteWidth, pathWidth = 0, 0, 0
		labelWidth = max(1, width-4)
	}

	var b strings.Builder
	switch {
	case selected && st.plain():
		b.WriteString("> ")
	case selected:
		b.WriteString(st.glyph(roleAccent, bg, st.displayText(st.g.bar)) + st.paintWith(roleText, bg, false, " "))
	default:
		b.WriteString("  ")
	}
	glyph := st.g.dot
	if !row.ws.Running {
		glyph = st.g.ring
	}
	b.WriteString(st.glyph(rowRole(row), bg, st.displayText(glyph)) + st.paintWith(roleText, bg, false, " "))
	labelRole := roleStrong
	if !row.ws.Running {
		labelRole = roleFaint
	}
	b.WriteString(st.lineOn([]span{{row.label, labelRole}}, labelWidth, bg, true))
	if pathWidth > 0 {
		b.WriteString(st.paintWith(roleText, bg, false, " ") + st.paintWith(roleDim, bg, false, fitLeft(homeRelative(row.ws.Path), pathWidth)))
	} else if metaWidth+noteWidth > 0 {
		b.WriteString(st.paintWith(roleText, bg, false, " "))
	}
	if metaWidth > 0 {
		// "claude 2m": provider in its color, the age as quiet metadata.
		text := truncateCell(meta, metaWidth)
		pad := strings.Repeat(" ", metaWidth-len([]rune(text)))
		b.WriteString(st.paintWith(roleText, bg, false, pad))
		if provider, age, ok := strings.Cut(text, " "); ok && providerRole(provider) != roleText {
			b.WriteString(st.paintWith(providerRole(provider), bg, false, provider) + st.paintWith(roleMeta, bg, false, " "+age))
		} else {
			b.WriteString(st.paintWith(roleMeta, bg, false, text))
		}
	}
	if noteWidth > 0 {
		note := fmt.Sprintf("%5s", rowNote(row))
		b.WriteString(st.paintWith(roleWarn, bg, false, note))
	}
	// Exact width: pad anything left on the selection background.
	used := 4 + labelWidth
	if pathWidth > 0 {
		used += 1 + pathWidth
	} else if metaWidth+noteWidth > 0 {
		used++
	}
	used += metaWidth + noteWidth
	if used < width {
		b.WriteString(st.paintWith(roleText, bg, false, strings.Repeat(" ", width-used)))
	}
	if used > width {
		return st.lineOn([]span{{row.label, labelRole}}, width, bg, true)
	}
	return b.String()
}

func renderEmptyState(st tuiStyle, width int) []string {
	return []string{
		"",
		st.lineOn([]span{{"  No workspaces yet", roleStrong}}, width, nil, true),
		"",
		st.lineOn([]span{{"  n", roleKeySafe}, {"  create one: pick this folder or a subfolder, or type a path", roleDim}}, width, nil, true),
		st.lineOn([]span{{"  or from a shell: ", roleDim}, {"agw add", roleAccent}, {", ", roleDim}, {"agw task new <name>", roleAccent}}, width, nil, true),
		"",
	}
}

func detailSubtitle(row tuiRow) []span {
	switch {
	case row.task != nil:
		return []span{{"task ", roleMeta}, {row.task.Name, roleAccent}, {" of ", roleMeta}, {homeRelative(row.task.Repo), roleText},
			{" · branch ", roleMeta}, {row.task.Branch, roleOK}}
	case filepath.IsAbs(row.ws.Path):
		return []span{{"project ", roleMeta}, {homeRelative(row.ws.Path), roleText}}
	}
	return []span{{"container ", roleMeta}, {row.ws.Name, roleText}}
}

func sessionSpans(row tuiRow, now time.Time, st tuiStyle) [][]span {
	return sessionSpansAligned(row, now, st, true)
}

func sessionSpansAligned(row tuiRow, now time.Time, st tuiStyle, aligned bool) [][]span {
	if !row.ws.Running {
		return [][]span{{{"stopped", roleFaint}, {" · enter, c or x starts it", roleDim}}}
	}
	if len(row.agent.Sessions) == 0 {
		return [][]span{{{"none running", roleText}, {" · c claude, x codex", roleDim}}}
	}
	var lines [][]span
	for _, s := range row.agent.Sessions {
		state, role := sessionState(s.State)
		provider, stateText := s.Provider+" ", state+" "
		if aligned {
			provider, stateText = fmt.Sprintf("%-8s", s.Provider), fmt.Sprintf("%-13s", state)
		}
		lines = append(lines, []span{{st.g.dot + " ", role}, {provider, providerRole(s.Provider)}, {stateText, role}, {shortAge(s.Time, now), roleMeta}})
	}
	return lines
}

// renderDetailPane is the right pane of the two-pane layout.
func renderDetailPane(view tuiView, width int) []string {
	st := view.style
	if len(view.rows) == 0 || view.selected < 0 || view.selected >= len(view.rows) {
		return nil
	}
	row := view.rows[view.selected]
	var lines [][]span
	// A title cannot be set in a larger font in a terminal: weight, color and
	// an underline rule give it the weight of a heading instead.
	titleRole := roleBrand
	if !row.ws.Running {
		titleRole = roleStrong
	}
	underline := strings.Repeat(st.g.hline, min(width, max(len([]rune(terminalText(row.label))), 3)))
	lines = append(lines, []span{{row.label, titleRole}}, []span{{underline, roleFaint}}, detailSubtitle(row), nil,
		[]span{{"AGENTS", roleHeading}})
	for _, line := range sessionSpans(row, view.now, st) {
		lines = append(lines, line)
	}
	lines = append(lines, nil, []span{{"WORKSPACE", roleHeading}})
	kv := func(key string, value ...span) []span {
		return append([]span{{fmt.Sprintf("%-8s", key), roleDim}}, value...)
	}
	state := []span{{st.g.dot + " ", roleOK}, {"running", roleText}}
	if !row.ws.Running {
		state = []span{{st.g.ring + " ", roleFaint}, {"stopped", roleFaint}}
	}
	lines = append(lines, kv("state", state...), kv("path", span{homeRelative(row.ws.Path), roleText}))
	if d := view.detail; d != nil {
		lines = append(lines, kv("files", d.files), kv("protect", d.protect...), kv("proxy", d.proxy...), kv("logins", d.logins), kv("layout", d.layout...))
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		// Glyph spans (the session dot) are trusted; others are escaped by line().
		out = append(out, st.line(line, width))
	}
	return out
}

func renderCompactDetail(view tuiView, width int) []string {
	st := view.style
	if len(view.rows) == 0 || view.selected < 0 || view.selected >= len(view.rows) {
		return []string{"", ""}
	}
	row := view.rows[view.selected]
	first := append([]span{{" " + row.label, roleStrong}, {"  ", roleText}}, detailSubtitle(row)...)
	second := []span{{" ", roleText}}
	for i, line := range sessionSpansAligned(row, view.now, st, false) {
		if i > 0 {
			second = append(second, span{"   ", roleText})
		}
		second = append(second, line...)
	}
	if d := view.detail; d != nil {
		second = append(second, span{"   proxy ", roleDim})
		second = append(second, d.proxy...)
	}
	return []string{st.line(first, width), st.line(second, width)}
}

type keyHint struct {
	key, label string
	role       tuiRole
}

// renderKeyHints follows the selection (lazygit-style contextual footer);
// "? help" and "q quit" always stay since help lists everything else.
func renderKeyHints(view tuiView, content int) string {
	st := view.style
	// Key colors group actions: open (blue), agents (mauve), bring results
	// back (green), stop (yellow), destroy (red), meta keys (plain bold).
	hints := []keyHint{{"enter", "shell", roleKeyMain}, {"c", "claude", roleKeyAgent}, {"x", "codex", roleKeyAgent}}
	if view.selected >= 0 && view.selected < len(view.rows) {
		row := view.rows[view.selected]
		if row.task != nil {
			hints = append(hints, keyHint{"f", "fetch", roleKeySafe})
		}
		if row.ws.Running {
			hints = append(hints, keyHint{"s", "stop", roleKeyStop})
		}
		hints = append(hints, keyHint{"d", "delete", roleError})
	} else {
		hints = nil
	}
	always := []keyHint{{"n", "new", roleKeySafe}, {"?", "help", roleStrong}, {"q", "quit", roleStrong}}
	return renderHints(st, hints, always, content)
}

// renderHints shows hints while they fit; always ones are kept at the end.
func renderHints(st tuiStyle, hints, always []keyHint, content int) string {
	size := func(h keyHint) int {
		if st.plain() {
			return len(h.key) + len(h.label) + 5 // "[k] label  "
		}
		return len(h.key) + len(h.label) + 3 // "k label  "
	}
	budget := content - 1
	for _, h := range always {
		budget -= size(h)
	}
	var shown []keyHint
	for _, h := range hints {
		if budget-size(h) < 0 {
			break
		}
		budget -= size(h)
		shown = append(shown, h)
	}
	var line strings.Builder
	line.WriteString(" ")
	used := 1
	for _, h := range append(shown, always...) {
		if used+size(h) > content {
			break
		}
		if st.plain() {
			line.WriteString("[" + h.key + "] " + h.label + "  ")
		} else {
			line.WriteString(st.paintWith(h.role, nil, true, h.key) + " " + st.role(roleDim, h.label) + "  ")
		}
		used += size(h)
	}
	return line.String()
}

func tuiHelpLines(st tuiStyle) [][]span {
	key := func(k, description string, role ...tuiRole) []span {
		keyRole := roleKeyMain
		if len(role) > 0 {
			keyRole = role[0]
		}
		return []span{{"   ", roleText}, {fmt.Sprintf("%-14s", k), keyRole}, {description, roleText}}
	}
	section := func(i int, description string) []span {
		glyph := st.g.dot
		if i == 4 {
			glyph = st.g.ring
		}
		return []span{{"   " + glyph + " ", tuiSections[i].role}, {fmt.Sprintf("%-12s", tuiSections[i].title), tuiSections[i].role}, {description, roleText}}
	}
	return [][]span{
		{{" KEYS", roleHeading}},
		key("up/down  j/k", "move", roleStrong),
		key("n", "new workspace: browse folders from here or type a path; project or task", roleKeySafe),
		key("enter", "open a shell (starts a stopped workspace)"),
		key("c / x", "run Claude Code / Codex in the selected workspace", roleKeyAgent),
		key("f", "fetch branch agw/<task> back into its repository", roleKeySafe),
		key("s", "stop the workspace and its traffic proxy", roleKeyStop),
		key("d", "delete the container; host files, logs, saved logins stay", roleError),
		key("r", "refresh now (also refreshes by itself)", roleStrong),
		key("q  esc", "quit; after a shell or agent exits you return here", roleStrong),
		nil,
		{{" SECTIONS", roleHeading}},
		section(0, "an agent is waiting for your answer"),
		section(1, "an agent finished its turn: review it or give the next prompt"),
		section(2, "an agent is processing; usually leave it alone"),
		section(3, "container running, no agent started with c/x or agw agent"),
		section(4, "container stopped; enter, c or x starts it"),
		nil,
		{{" old", roleWarn}, {"  created by an older AgentWisp; run ", roleText}, {"agw doctor", roleAccent}, {" for what recreating adds", roleText}},
	}
}
