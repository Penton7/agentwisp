package agentwisp

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
)

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
	case 'd', 'D':
		return keyDelete, nil
	case 'y', 'Y':
		return keyConfirm, nil
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
