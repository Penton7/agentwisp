package agentwisp

import "strings"

// Colors carry meaning (role), never information that is only visible in
// color: section headers, state words and the selection marker keep the UI
// usable with NO_COLOR (https://no-color.org) or monochrome terminals.

type tuiRole int

const (
	roleText tuiRole = iota
	roleDim
	roleFaint
	roleStrong
	roleBrand
	roleAccent
	roleOK
	roleWarn
	roleError
	roleUrgent
	roleWorking
	roleIdle
	roleRule
	roleMeta    // Ages and connective words: secondary italic.
	roleHeading // Pane and section titles: bold.
	roleClaude
	roleCodex
	roleKeyMain  // enter: open
	roleKeyAgent // c / x: start agents
	roleKeySafe  // f: fetch results
	roleKeyStop  // s: stop
)

type tuiStyle struct {
	mode colorMode
	pal  palette
	g    glyphs
}

// plainStyle renders text only: tests and NO_COLOR.
func plainStyle() tuiStyle { return tuiStyle{mode: colorNone, pal: darkPalette, g: unicodeGlyphs} }

func (st tuiStyle) plain() bool { return st.mode == colorNone }

type roleLook struct {
	color        rgb
	colored      bool
	bold, italic bool
}

func (st tuiStyle) look(role tuiRole) roleLook {
	p := st.pal
	c := func(color rgb) roleLook { return roleLook{color: color, colored: true} }
	switch role {
	case roleDim:
		return c(p.dim)
	case roleFaint, roleRule:
		return c(p.faint)
	case roleStrong, roleHeading:
		return roleLook{bold: true}
	case roleMeta:
		l := c(p.dim)
		l.italic = true
		return l
	case roleBrand:
		l := c(p.brand)
		l.bold = true
		return l
	case roleAccent:
		return c(p.accent)
	case roleOK, roleIdle:
		return c(p.ready)
	case roleWarn:
		return c(p.warn)
	case roleError:
		l := c(p.danger)
		l.bold = true
		return l
	case roleUrgent:
		l := c(p.urgent)
		l.bold = true
		return l
	case roleWorking:
		return c(p.working)
	case roleClaude:
		return c(p.claude)
	case roleCodex:
		return c(p.codex)
	case roleKeyMain:
		l := c(p.accent)
		l.bold = true
		return l
	case roleKeyAgent:
		l := c(p.brand)
		l.bold = true
		return l
	case roleKeySafe:
		l := c(p.ready)
		l.bold = true
		return l
	case roleKeyStop:
		l := c(p.warn)
		l.bold = true
		return l
	}
	return roleLook{}
}

func providerRole(provider string) tuiRole {
	switch AgentProvider(provider) {
	case ProviderClaude:
		return roleClaude
	case ProviderCodex:
		return roleCodex
	}
	return roleText
}

func (st tuiStyle) sgr(role tuiRole, bg *rgb, bold bool) string {
	if st.plain() {
		return ""
	}
	var codes []string
	l := st.look(role)
	if l.bold || bold {
		codes = append(codes, "1")
	}
	if l.italic {
		codes = append(codes, "3")
	}
	if l.colored {
		codes = append(codes, st.fgCode(l.color))
	}
	if bg != nil {
		codes = append(codes, st.bgCode(*bg))
	}
	return strings.Join(codes, ";")
}

func (st tuiStyle) paintWith(role tuiRole, bg *rgb, bold bool, text string) string {
	code := st.sgr(role, bg, bold)
	if code == "" || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (st tuiStyle) role(role tuiRole, text string) string {
	return st.paintWith(role, nil, false, text)
}

type span struct {
	text string
	role tuiRole
}

// line renders spans within width cells: each span is escaped, the line is
// cut with "…" when too long, and colors wrap only visible text. With pad,
// the line is filled to exactly width (on bg when given).
func (st tuiStyle) line(spans []span, width int) string { return st.lineOn(spans, width, nil, false) }

func (st tuiStyle) lineOn(spans []span, width int, bg *rgb, pad bool) string {
	var out strings.Builder
	used := 0
	for i, s := range spans {
		runes := []rune(st.displayText(s.text))
		room := width - used
		if room <= 0 {
			break
		}
		if len(runes) > room || (len(runes) == room && hasVisibleAfter(spans[i+1:])) {
			out.WriteString(st.paintWith(s.role, bg, false, string(runes[:max(0, room-1)])+st.displayText("…")))
			used = width
			break
		}
		out.WriteString(st.paintWith(s.role, bg, false, string(runes)))
		used += len(runes)
	}
	if pad && used < width {
		out.WriteString(st.paintWith(roleText, bg, false, strings.Repeat(" ", width-used)))
	}
	return out.String()
}

// glyph paints one of our own trusted one-cell glyphs (already passed
// through displayText for the ASCII fallback).
func (st tuiStyle) glyph(role tuiRole, bg *rgb, glyph string) string {
	return st.paintWith(role, bg, false, glyph)
}

func hasVisibleAfter(spans []span) bool {
	for _, s := range spans {
		if s.text != "" {
			return true
		}
	}
	return false
}

// trustedGlyphs are the only non-ASCII characters the manager prints as-is:
// one-cell shapes it uses itself. Everything else non-ASCII is escaped by
// terminalText, so untrusted names cannot break alignment or the terminal.
var trustedGlyphs = map[rune]string{'●': "*", '○': "o", '▌': ">", '│': "|", '─': "-", '·': "-", '…': ".", '█': "_", '→': ">", '←': "<"}

func (st tuiStyle) displayText(value string) string {
	var out strings.Builder
	for _, r := range value {
		if ascii, ok := trustedGlyphs[r]; ok {
			if st.g == asciiGlyphs {
				out.WriteString(ascii)
			} else {
				out.WriteRune(r)
			}
			continue
		}
		out.WriteString(terminalText(string(r)))
	}
	return out.String()
}

func spansText(spans []span) string {
	var out strings.Builder
	for _, s := range spans {
		out.WriteString(s.text)
	}
	return out.String()
}

func (st tuiStyle) rule(width int) string {
	return st.role(roleRule, strings.Repeat(st.g.hline, max(0, width)))
}
