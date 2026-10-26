package agentwisp

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

var tuiNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func colorStyle(pal palette) tuiStyle { return tuiStyle{mode: colorTrue, pal: pal, g: unicodeGlyphs} }

func sampleRows() []tuiRow {
	workspaces := []Workspace{
		{Name: "codex-work-legacy", Path: "/old/legacy"},
		{Name: "agentwisp-work-web-9f8e7d6c5b4a3921", Path: "/code/web", Running: true, Layout: 2},
		{Name: "agentwisp-work-api-fix-1a2b3c4d5e6f7a8b", Path: "/code/api-fix", Running: true, Layout: 2},
		{Name: "agentwisp-work-idle-0000000000000000", Path: "/code/idle", Running: true, Layout: 2},
		{Name: "agentwisp-work-docs-1111111111111111", Path: "/code/docs", Running: true, Layout: 2},
	}
	agents := map[string]agentInfo{
		"agentwisp-work-web-9f8e7d6c5b4a3921": {State: "working", Since: tuiNow.Add(-14 * time.Minute),
			Sessions: []agentStatus{{Provider: "codex", State: "working", Time: tuiNow.Add(-14 * time.Minute)}}},
		"agentwisp-work-api-fix-1a2b3c4d5e6f7a8b": {State: "needs-input", Since: tuiNow.Add(-2 * time.Minute), Waiting: 1,
			Sessions: []agentStatus{{Provider: "claude", State: "needs-input", Time: tuiNow.Add(-2 * time.Minute)}}},
		"agentwisp-work-docs-1111111111111111": {State: "idle", Since: tuiNow.Add(-5 * time.Minute),
			Sessions: []agentStatus{{Provider: "claude", State: "idle", Time: tuiNow.Add(-5 * time.Minute)}}},
	}
	tasks := map[string]taskRecord{"/code/api-fix": {Name: "fix", Repo: "/code/api", Path: "/code/api-fix", Branch: "agw/fix"}}
	return buildTUIRows(workspaces, agents, tasks)
}

func sampleDetail() *tuiDetail {
	return &tuiDetail{
		proxy:  []span{{"on", roleText}, {" · ", roleFaint}, {"allowlist 3 host(s)", roleText}, {" · ", roleFaint}, {"up", roleOK}},
		logins: span{"saved on host", roleText},
		files:  span{"isolated clone, main .git not mounted", roleText},
		layout: []span{{"2", roleText}, {" · current", roleDim}},
	}
}

func renderLines(view tuiView) []string {
	var frame strings.Builder
	renderTUIView(&frame, view)
	return strings.Split(strings.TrimPrefix(frame.String(), "\x1b[H\x1b[2J"), "\n")
}

func TestTUISectionsFollowAttention(t *testing.T) {
	rows := sampleRows()
	var labels []string
	for _, row := range rows {
		labels = append(labels, row.label)
	}
	if got := strings.Join(labels, ","); got != "api / fix,docs,web,idle,legacy" {
		t.Fatalf("order: %s", got)
	}
	text := strings.Join(renderLines(tuiView{rows: rows, width: 90, now: tuiNow, style: plainStyle()}), "\n")
	last := -1
	for _, title := range []string{"NEEDS YOU", "READY", "WORKING", "NO AGENT", "STOPPED"} {
		i := strings.Index(text, title)
		if i < last {
			t.Fatalf("section %s out of order:\n%s", title, text)
		}
		last = i
	}
	if agentMeta(rows[0], tuiNow) != "claude 2m" || agentMeta(rows[3], tuiNow) != "" {
		t.Fatalf("meta: %q %q", agentMeta(rows[0], tuiNow), agentMeta(rows[3], tuiNow))
	}

	dup := buildTUIRows([]Workspace{
		{Name: "agentwisp-work-x-0123456789abcdef", Path: "-"},
		{Name: "agentwisp-work-api-1111111111111111", Path: "/work/api"},
		{Name: "agentwisp-work-api-2222222222222222", Path: "/home/api"},
	}, nil, nil)
	if got := dup[0].label + "," + dup[1].label + "," + dup[2].label; got != "home/api,work/api,x" {
		t.Fatalf("labels %q", got)
	}
}

func TestTUIWidthInvariantsAllLayouts(t *testing.T) {
	rows := sampleRows()
	styles := map[string]tuiStyle{"plain": plainStyle(), "dark": colorStyle(darkPalette), "light-256": {mode: color256, pal: lightPalette, g: unicodeGlyphs}, "ascii": {mode: color256, pal: darkPalette, g: asciiGlyphs}}
	for name, st := range styles {
		for _, width := range []int{2, 12, 30, 45, 60, 80, 99, 100, 130, 200} {
			for _, height := range []int{0, 12, 24} {
				view := tuiView{rows: rows, selected: 2, width: width, height: height, now: tuiNow, detail: sampleDetail(), message: "Fetched agw/fix", messageRole: roleOK, style: st}
				view.offset = scrollOffset(view)
				lines := renderLines(view)
				for i, line := range lines {
					if w := visibleWidth(line); w >= width && width > 1 {
						t.Fatalf("%s %dx%d line %d reaches wrap column (%d): %q", name, width, height, i, w, line)
					}
				}
				if height > 0 && len(lines) > height+1 {
					t.Fatalf("%s %dx%d: %d lines exceed height", name, width, height, len(lines))
				}
			}
		}
	}
}

func TestTUITwoPaneAndCompactLayouts(t *testing.T) {
	rows := sampleRows()
	wide := strings.Join(renderLines(tuiView{rows: rows, selected: 0, width: 130, now: tuiNow, detail: sampleDetail(), style: plainStyle()}), "\n")
	for _, want := range []string{"│", "AGENTS", "WORKSPACE", "needs input", "allowlist 3 host(s)", "saved on host", "branch agw/fix"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("two-pane misses %q:\n%s", want, wide)
		}
	}
	// List rows in the two-pane layout have exactly the list width.
	l := layoutFor(tuiView{width: 130})
	for _, line := range renderList(tuiView{rows: rows, width: 130, now: tuiNow, style: colorStyle(darkPalette)}, l) {
		if visibleWidth(line) != l.listWidth {
			t.Fatalf("list line width %d != %d: %q", visibleWidth(line), l.listWidth, line)
		}
	}
	narrow := strings.Join(renderLines(tuiView{rows: rows, selected: 0, width: 80, now: tuiNow, detail: sampleDetail(), style: plainStyle()}), "\n")
	if strings.Contains(narrow, "│") || !strings.Contains(narrow, "~") && !strings.Contains(narrow, "/code/") {
		t.Fatalf("compact layout:\n%s", narrow)
	}
	if !strings.Contains(narrow, "claude") || !strings.Contains(narrow, "needs input") || !strings.Contains(narrow, "/code/api-fix") {
		t.Fatalf("compact detail lacks sessions:\n%s", narrow)
	}
}

func TestTUIScrollKeepsSelectionAndHeaderVisible(t *testing.T) {
	var workspaces []Workspace
	for i := 0; i < 30; i++ {
		workspaces = append(workspaces, Workspace{Name: fmt.Sprintf("agentwisp-work-p%02d", i), Path: fmt.Sprintf("/code/p%02d", i), Running: i%3 != 0, Layout: 2})
	}
	view := tuiView{rows: buildTUIRows(workspaces, nil, nil), width: 80, height: 18, now: tuiNow, style: plainStyle()}
	for _, selected := range []int{0, 12, 29, 5, 19, 20} {
		view.selected = selected
		view.offset = scrollOffset(view)
		text := strings.Join(renderLines(view), "\n")
		if !strings.Contains(text, "> ") || !strings.Contains(text, view.rows[selected].label) {
			t.Fatalf("selected row %d not visible:\n%s", selected, text)
		}
	}
	// The first row of a section brings its header into view.
	view.selected = 20 // First stopped row (20 running rows sort first).
	view.offset = scrollOffset(view)
	if text := strings.Join(renderLines(view), "\n"); !strings.Contains(text, "STOPPED") {
		t.Fatalf("section header hidden:\n%s", text)
	}
}

func TestTUIStylesAndNoColor(t *testing.T) {
	rows := sampleRows()
	var frame strings.Builder
	renderTUIView(&frame, tuiView{rows: rows, selected: 0, width: 130, now: tuiNow, detail: sampleDetail(), style: plainStyle()})
	if strings.Contains(strings.TrimPrefix(frame.String(), "\x1b[H\x1b[2J"), "\x1b[") {
		t.Fatal("NO_COLOR frame contains SGR")
	}
	frame.Reset()
	dark := colorStyle(darkPalette)
	renderTUIView(&frame, tuiView{rows: rows, selected: 0, width: 130, now: tuiNow, detail: sampleDetail(), style: dark, message: "boom", messageRole: roleError})
	text := frame.String()
	for _, want := range []string{dark.fgCode(darkPalette.urgent), dark.bgCode(darkPalette.selBG), dark.fgCode(darkPalette.danger) + "m boom", dark.fgCode(darkPalette.brand)} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing style %q", want)
		}
	}
	if !strings.Contains(renderKeyHints(tuiView{rows: rows, style: plainStyle()}, 120), "[enter] shell") {
		t.Fatal("plain key hints")
	}
	narrow := renderKeyHints(tuiView{rows: rows, style: dark}, 40)
	if !strings.Contains(narrow, "help") || !strings.Contains(narrow, "quit") || visibleWidth(narrow) > 40 {
		t.Fatalf("narrow hints: %q", narrow)
	}
	ascii := tuiStyle{mode: color256, pal: darkPalette, g: asciiGlyphs}
	frame.Reset()
	renderTUIView(&frame, tuiView{rows: rows, selected: 0, width: 130, now: tuiNow, detail: sampleDetail(), style: ascii})
	for _, r := range frame.String() {
		if r > 127 {
			t.Fatalf("ASCII mode printed %q", r)
		}
	}
}

func TestTUIUntrustedTextStaysEscaped(t *testing.T) {
	rows := buildTUIRows([]Workspace{{Name: "agentwisp-work-x", Path: "/p/\x1b]52;c;evil\a项目●", Running: true}}, nil, nil)
	for _, st := range []tuiStyle{plainStyle(), colorStyle(lightPalette)} {
		var frame strings.Builder
		renderTUIView(&frame, tuiView{rows: rows, width: 130, now: tuiNow, style: st, detail: sampleDetail()})
		if strings.Contains(frame.String(), "\x1b]52") || strings.Contains(frame.String(), "项目") {
			t.Fatal("untrusted text reached the terminal")
		}
	}
}

func TestHelpAndDeleteScreens(t *testing.T) {
	help := strings.Join(renderLines(tuiView{help: true, width: 100, style: plainStyle()}), "\n")
	for _, want := range []string{"c / x", "NEEDS YOU", "READY", "agw doctor", "press any key"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help misses %q", want)
		}
	}
	var out bytes.Buffer
	renderDeleteConfirmation(&out, sampleRows()[0], 100, plainStyle())
	if !strings.Contains(out.String(), "Delete api / fix?") || !strings.Contains(out.String(), "agw task rm fix") {
		t.Fatalf("%s", out.String())
	}
	empty := strings.Join(renderLines(tuiView{width: 100, style: plainStyle()}), "\n")
	if !strings.Contains(empty, "No workspaces yet") || !strings.Contains(empty, "agw task new") {
		t.Fatalf("empty state:\n%s", empty)
	}
}

func TestThemeDetection(t *testing.T) {
	env := func(values map[string]string) func(string) string { return func(k string) string { return values[k] } }
	if detectColorMode(env(map[string]string{"NO_COLOR": "1", "COLORTERM": "truecolor"})) != colorNone ||
		detectColorMode(env(map[string]string{"TERM": "dumb"})) != colorNone ||
		detectColorMode(env(map[string]string{"COLORTERM": "truecolor"})) != colorTrue ||
		detectColorMode(env(nil)) != color256 {
		t.Fatal("color mode")
	}
	for value, want := range map[string]string{"15;0": "dark", "0;15": "light", "0;7": "light", "7;8": "dark"} {
		if pal, ok := themeFromEnv(env(map[string]string{"COLORFGBG": value})); !ok || pal.name != want {
			t.Fatalf("COLORFGBG %s: %s", value, pal.name)
		}
	}
	if pal, ok := themeFromEnv(env(map[string]string{"AGENTWISP_THEME": "light", "COLORFGBG": "15;0"})); !ok || pal.name != "light" {
		t.Fatal("explicit theme must win")
	}
	if _, ok := themeFromEnv(env(nil)); ok {
		t.Fatal("unknown background must ask the terminal")
	}
	for reply, want := range map[string]bool{"\x1b]11;rgb:ffff/ffff/ffff\x07": true, "\x1b]11;rgb:1e1e/1e1e/2e2e\x1b\\": false, "\x1b]11;rgb:ee/ee/ee\x07": true} {
		if light, ok := parseOSC11(reply); !ok || light != want {
			t.Fatalf("%q: %v %v", reply, light, ok)
		}
	}
	for _, bad := range []string{"", "\x1b]11;rgb:zz/00/00\x07", "rgb:1/2"} {
		if _, ok := parseOSC11(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
	if detectGlyphs(env(map[string]string{"LANG": "en_US.UTF-8"})) != unicodeGlyphs || detectGlyphs(env(map[string]string{"LANG": "C"})) != asciiGlyphs ||
		detectGlyphs(env(map[string]string{"LANG": "en_US.UTF-8", "AGENTWISP_ASCII": "1"})) != asciiGlyphs {
		t.Fatal("glyphs")
	}
	if nearest256(rgb{0, 0, 0}) != 16 || nearest256(rgb{255, 255, 255}) != 231 || nearest256(rgb{128, 128, 128}) != 244 {
		t.Fatalf("nearest256: %d %d %d", nearest256(rgb{0, 0, 0}), nearest256(rgb{255, 255, 255}), nearest256(rgb{128, 128, 128}))
	}
}

func TestLateOSCReplyIsNotEscape(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\x1b]11;rgb:ffff/ffff/ffff\x07j"))
	if key, _ := readTUIKey(reader); key != keyUnknown {
		t.Fatalf("OSC reply read as %v", key)
	}
	if key, _ := readTUIKey(reader); key != keyDown {
		t.Fatal("key after OSC lost")
	}
}

// Guards palette edits: every role must stay readable on representative
// backgrounds of its theme (WCAG contrast ratio).
func TestPaletteContrast(t *testing.T) {
	luminance := func(c rgb) float64 {
		channel := func(v uint8) float64 {
			f := float64(v) / 255
			if f <= 0.03928 {
				return f / 12.92
			}
			return math.Pow((f+0.055)/1.055, 2.4)
		}
		return 0.2126*channel(c.r) + 0.7152*channel(c.g) + 0.0722*channel(c.b)
	}
	contrast := func(a, b rgb) float64 {
		la, lb := luminance(a), luminance(b)
		return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
	}
	check := func(pal palette, backgrounds []string) {
		for _, bgHex := range backgrounds {
			bg := hexRGB(bgHex)
			for name, c := range map[string]rgb{"dim": pal.dim, "brand": pal.brand, "accent": pal.accent, "urgent": pal.urgent,
				"ready": pal.ready, "working": pal.working, "warn": pal.warn, "danger": pal.danger, "claude": pal.claude, "codex": pal.codex} {
				if r := contrast(c, bg); r < 3.6 {
					t.Errorf("%s %s on %s: %.1f < 3.6", pal.name, name, bgHex, r)
				}
				if r := contrast(c, pal.selBG); r < 2.7 {
					t.Errorf("%s %s on selection: %.1f < 2.7", pal.name, name, r)
				}
			}
			if r := contrast(pal.faint, bg); r < 3.0 {
				t.Errorf("%s faint on %s: %.1f < 3.0", pal.name, bgHex, r)
			}
		}
	}
	check(darkPalette, []string{"#1e1e2e", "#000000", "#282c34"})
	check(lightPalette, []string{"#ffffff", "#eff1f5"})
}
