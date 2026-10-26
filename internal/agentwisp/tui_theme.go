package agentwisp

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Manager theme: Catppuccin (https://catppuccin.com) Mocha on dark
// backgrounds and Latte on light ones. Latte accents are darkened where the
// originals fall below ~3.5:1 on white (peach, teal, green, sapphire, pink,
// lavender), since terminal text is small; TestPaletteContrast guards the
// minimums. Warnings use teal rather than yellow, by preference.
//
// Plain text keeps the terminal's own foreground so the manager blends into
// the user's theme; only roles with meaning get colors.

type rgb struct{ r, g, b uint8 }

func hexRGB(hex string) rgb {
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil || len(strings.TrimPrefix(hex, "#")) != 6 {
		panic("invalid palette color " + hex)
	}
	return rgb{uint8(v >> 16), uint8(v >> 8), uint8(v)}
}

type palette struct {
	name    string
	dim     rgb // Secondary text: labels, paths, hints.
	faint   rgb // De-emphasized: stopped rows, rules, section counts.
	brand   rgb
	accent  rgb // Keys, commands, names worth noticing.
	urgent  rgb // Needs you.
	ready   rgb // Finished a turn; also success.
	working rgb
	warn    rgb
	danger  rgb
	claude  rgb // Provider names, so sessions are told apart at a glance.
	codex   rgb
	selBG   rgb // Selected row background.
}

var darkPalette = palette{
	name:    "dark",
	dim:     hexRGB("#bac2de"), // subtext1: secondary text must stay easy to read
	faint:   hexRGB("#9399b2"), // overlay2
	brand:   hexRGB("#cba6f7"), // mauve
	accent:  hexRGB("#89b4fa"), // blue
	urgent:  hexRGB("#fab387"), // peach
	ready:   hexRGB("#a6e3a1"), // green
	working: hexRGB("#74c7ec"), // sapphire
	warn:    hexRGB("#94e2d5"), // teal: greener than "working" sapphire
	danger:  hexRGB("#f38ba8"), // red
	claude:  hexRGB("#f5c2e7"), // pink
	codex:   hexRGB("#b4befe"), // lavender
	selBG:   hexRGB("#313244"), // surface0
}

var lightPalette = palette{
	name:    "light",
	dim:     hexRGB("#4f5370"), // between text and subtext1
	faint:   hexRGB("#6c6f85"), // subtext0
	brand:   hexRGB("#8839ef"), // mauve
	accent:  hexRGB("#1e66f5"), // blue
	urgent:  hexRGB("#d94f00"), // peach, darkened
	ready:   hexRGB("#2f7d1e"), // green, darkened
	working: hexRGB("#13778a"), // sapphire, darkened
	warn:    hexRGB("#0d7d72"), // teal, darkened and shifted green away from sapphire
	danger:  hexRGB("#d20f39"), // red
	claude:  hexRGB("#b8389a"), // pink, darkened
	codex:   hexRGB("#4f63d8"), // lavender, darkened
	selBG:   hexRGB("#dce0e8"), // crust
}

type colorMode int

const (
	colorNone colorMode = iota
	color256
	colorTrue
)

// glyphs: a small fixed set of box-drawing and shape characters that occupy
// one cell in non-CJK UTF-8 terminals. Untrusted text never uses them (it is
// escaped to ASCII); a non-UTF-8 locale or AGENTWISP_ASCII=1 gets ASCII.
type glyphs struct {
	dot, ring, bar, vline, hline, cursor string
}

var unicodeGlyphs = glyphs{dot: "●", ring: "○", bar: "▌", vline: "│", hline: "─", cursor: "█"}
var asciiGlyphs = glyphs{dot: "*", ring: "o", bar: ">", vline: "|", hline: "-", cursor: "_"}

func detectColorMode(getenv func(string) string) colorMode {
	if getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
		return colorNone
	}
	switch strings.ToLower(getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return colorTrue
	}
	return color256
}

func detectGlyphs(getenv func(string) string) glyphs {
	if getenv("AGENTWISP_ASCII") == "1" {
		return asciiGlyphs
	}
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if value := getenv(key); value != "" {
			if v := strings.ToLower(value); strings.Contains(v, "utf-8") || strings.Contains(v, "utf8") {
				return unicodeGlyphs
			}
			return asciiGlyphs
		}
	}
	return asciiGlyphs
}

// themeFromEnv resolves dark/light without touching the terminal; ok=false
// means "ask the terminal" (OSC 11) before falling back to dark.
func themeFromEnv(getenv func(string) string) (palette, bool) {
	switch strings.ToLower(getenv("AGENTWISP_THEME")) {
	case "dark":
		return darkPalette, true
	case "light":
		return lightPalette, true
	}
	// COLORFGBG="fg;bg" (rxvt, iTerm2, Konsole): bg 7 or 9-15 is light.
	if value := getenv("COLORFGBG"); value != "" {
		fields := strings.Split(value, ";")
		if bg, err := strconv.Atoi(fields[len(fields)-1]); err == nil {
			if bg == 7 || (bg >= 9 && bg <= 15) {
				return lightPalette, true
			}
			return darkPalette, true
		}
	}
	return darkPalette, false
}

// parseOSC11 reads an "rgb:RRRR/GGGG/BBBB" background report (1-4 hex
// digits per channel) and says whether that background is light.
func parseOSC11(reply string) (light, ok bool) {
	start := strings.Index(reply, "rgb:")
	if start < 0 {
		return false, false
	}
	body := reply[start+4:]
	if end := strings.IndexAny(body, "\x07\x1b"); end >= 0 {
		body = body[:end]
	}
	parts := strings.Split(body, "/")
	if len(parts) != 3 {
		return false, false
	}
	var channel [3]float64
	for i, part := range parts {
		if len(part) == 0 || len(part) > 4 {
			return false, false
		}
		v, err := strconv.ParseUint(part, 16, 16)
		if err != nil {
			return false, false
		}
		channel[i] = float64(v) / float64(uint64(1)<<(4*len(part))-1)
	}
	luminance := 0.2126*channel[0] + 0.7152*channel[1] + 0.0722*channel[2]
	return luminance > 0.5, true
}

// managerStyle builds the style once per process: the OSC 11 probe runs at
// most once, before the key reader starts.
func managerStyle(probe func() (light, ok bool)) tuiStyle {
	st := tuiStyle{mode: detectColorMode(os.Getenv), g: detectGlyphs(os.Getenv)}
	if st.mode == colorNone {
		st.pal = darkPalette
		return st
	}
	pal, known := themeFromEnv(os.Getenv)
	if !known && probe != nil {
		if light, ok := probe(); ok && light {
			pal = lightPalette
		}
	}
	st.pal = pal
	return st
}

// xterm 256-color approximation for terminals without truecolor.
func nearest256(c rgb) int {
	levels := []int{0, 95, 135, 175, 215, 255}
	nearestLevel := func(v uint8) int {
		best := 0
		for i, l := range levels {
			if abs(int(v)-l) < abs(int(v)-levels[best]) {
				best = i
			}
		}
		return best
	}
	r, g, b := nearestLevel(c.r), nearestLevel(c.g), nearestLevel(c.b)
	cube := 16 + 36*r + 6*g + b
	cubeDist := dist(c, rgb{uint8(levels[r]), uint8(levels[g]), uint8(levels[b])})
	gray := (int(c.r) + int(c.g) + int(c.b)) / 3
	grayIndex := min(23, max(0, (gray-8+5)/10))
	grayValue := uint8(8 + grayIndex*10)
	if dist(c, rgb{grayValue, grayValue, grayValue}) < cubeDist {
		return 232 + grayIndex
	}
	return cube
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func dist(a, b rgb) int {
	dr, dg, db := int(a.r)-int(b.r), int(a.g)-int(b.g), int(a.b)-int(b.b)
	return dr*dr + dg*dg + db*db
}

func (st tuiStyle) fgCode(c rgb) string {
	if st.mode == colorTrue {
		return fmt.Sprintf("38;2;%d;%d;%d", c.r, c.g, c.b)
	}
	return fmt.Sprintf("38;5;%d", nearest256(c))
}

func (st tuiStyle) bgCode(c rgb) string {
	if st.mode == colorTrue {
		return fmt.Sprintf("48;2;%d;%d;%d", c.r, c.g, c.b)
	}
	return fmt.Sprintf("48;5;%d", nearest256(c))
}
