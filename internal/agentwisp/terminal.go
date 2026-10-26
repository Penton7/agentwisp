package agentwisp

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

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
	keyDelete
	keyConfirm
	keyQuit
	keyClaude
	keyCodex
	keyFetch
	keyHelp
	keyNew
	keyTask
	keySlash
	keyEscape    // Quits the list; goes back in the new-workspace screen.
	keyBackspace // Text input only.
	keyTab
	keyClear // Ctrl-U.
	keyLeft
	keyRight
)

// keyEvent is a key plus the character typed, for text input. Shortcut keys
// (j, k, q...) still carry their character, so typing a path is not hijacked.
type keyEvent struct {
	key tuiKey
	ch  rune
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
	event, err := readKeyEvent(reader)
	if event.key == keyEscape {
		event.key = keyQuit
	}
	return event.key, err
}

func readKeyEvent(reader *bufio.Reader) (keyEvent, error) {
	first, err := reader.ReadByte()
	if err != nil {
		return keyEvent{}, err
	}
	if first >= 0x80 {
		// A UTF-8 character typed into a path.
		_ = reader.UnreadByte()
		r, _, err := reader.ReadRune()
		return keyEvent{key: keyUnknown, ch: r}, err
	}
	event := keyEvent{key: keyUnknown}
	if first >= 32 && first < 127 {
		event.ch = rune(first)
	}
	switch first {
	case '\r', '\n':
		event.key = keyEnter
	case 'k', 'K':
		event.key = keyUp
	case 'j', 'J':
		event.key = keyDown
	case 'r', 'R':
		event.key = keyRefresh
	case 'd', 'D':
		event.key = keyDelete
	case 'y', 'Y':
		event.key = keyConfirm
	case 's', 'S':
		event.key = keyStop
	case 'c', 'C':
		event.key = keyClaude
	case 'x', 'X':
		event.key = keyCodex
	case 'f', 'F':
		event.key = keyFetch
	case 'n', 'N':
		event.key = keyNew
	case 'h':
		event.key = keyLeft
	case 'l':
		event.key = keyRight
	case 't', 'T':
		event.key = keyTask
	case '/':
		event.key = keySlash
	case '?':
		event.key = keyHelp
	case 'q', 'Q', 3:
		event.key = keyQuit
	case 127, 8:
		event.key = keyBackspace
	case 9:
		event.key = keyTab
	case 21:
		event.key = keyClear
	case 27:
		event.key = keyEscape
		if reader.Buffered() == 0 {
			return event, nil
		}
		second, err := reader.ReadByte()
		if err != nil {
			return event, nil
		}
		if second == ']' {
			// A late OSC reply (e.g. to the background query) is not Escape.
			for {
				b, err := reader.ReadByte()
				if err != nil || b == 0x07 {
					return keyEvent{}, nil
				}
				if b == 27 {
					_, _ = reader.ReadByte() // The "\\" of ESC \.
					return keyEvent{}, nil
				}
			}
		}
		if second == 27 {
			_ = reader.UnreadByte() // Escape pressed twice.
			return event, nil
		}
		if second != '[' && second != 'O' {
			return event, nil
		}
		third, err := reader.ReadByte()
		if err != nil {
			return keyEvent{}, nil
		}
		switch third {
		case 'A':
			return keyEvent{key: keyUp}, nil
		case 'B':
			return keyEvent{key: keyDown}, nil
		case 'C':
			return keyEvent{key: keyRight}, nil
		case 'D':
			return keyEvent{key: keyLeft}, nil
		}
		// Other sequences (right/left, Home, F-keys): consume digits and
		// modifiers up to the final byte so they do not leak into input.
		for b := third; (b >= '0' && b <= '9') || b == ';'; {
			if b, err = reader.ReadByte(); err != nil {
				break
			}
		}
		return keyEvent{}, nil
	}
	return event, nil
}

func terminalWidth() int {
	_, width := terminalSize()
	return width
}

// terminalSize returns rows and columns; rows is 0 when unknown, which turns
// off list scrolling instead of guessing.
func terminalSize() (int, int) {
	cmd := exec.Command("stty", "size")
	cmd.Stdin = os.Stdin
	output, err := cmd.Output()
	if err == nil {
		fields := strings.Fields(string(output))
		if len(fields) == 2 {
			rows, rowErr := strconv.Atoi(fields[0])
			width, widthErr := strconv.Atoi(fields[1])
			if rowErr == nil && widthErr == nil && width > 0 {
				return max(rows, 0), width
			}
		}
	}
	rows, _ := strconv.Atoi(os.Getenv("LINES"))
	if width, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && width > 0 {
		return max(rows, 0), width
	}
	return max(rows, 0), 80
}

func stty(args ...string) error {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// probeTerminalBackground asks the terminal for its background (OSC 11).
// Reads time out (VMIN 0, VTIME 2), so terminals that do not answer cost at
// most ~0.3 s and never block the manager. Call after enterRawTerminal.
func probeTerminalBackground() (light, ok bool) {
	if err := stty("min", "0", "time", "2"); err != nil {
		return false, false
	}
	defer func() { _ = stty("min", "1", "time", "0") }()
	if _, err := os.Stdout.WriteString("\x1b]11;?\x07"); err != nil {
		return false, false
	}
	reply := make([]byte, 0, 64)
	chunk := make([]byte, 64)
	for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline) && len(reply) < 256; {
		n, err := os.Stdin.Read(chunk)
		if n <= 0 || err != nil {
			break
		}
		reply = append(reply, chunk[:n]...)
		if bytes.IndexByte(reply, 0x07) >= 0 || bytes.Contains(reply, []byte("\x1b\\")) {
			break
		}
	}
	return parseOSC11(string(reply))
}
