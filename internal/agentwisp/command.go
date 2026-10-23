package agentwisp

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func commandOutput(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.Output()
	if err == nil {
		return output, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil, fmt.Errorf("%s CLI not found: %w", name, err)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		message := strings.TrimSpace(string(exitErr.Stderr))
		if message != "" {
			return nil, fmt.Errorf("%s %s: %s: %w", name, strings.Join(args, " "), message, err)
		}
	}
	return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
}

// commandStream copies stdout to a writer and keeps a bounded stderr excerpt
// for the error message, so large exports are never buffered in memory.
func commandStream(stdout io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var stderr limitedBuffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s CLI not found: %w", name, err)
	}
	if message := strings.TrimSpace(stderr.String()); message != "" {
		return fmt.Errorf("%s %s: %s: %w", name, args[0], message, err)
	}
	return fmt.Errorf("%s %s: %w", name, args[0], err)
}

type limitedBuffer struct{ data []byte }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := 4096 - len(b.data); room > 0 {
		b.data = append(b.data, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
func (b *limitedBuffer) String() string { return string(b.data) }

func execInteractive(errOut io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = errOut
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%s CLI not found: %w", name, err)
		}
		// Interactive arguments may contain prompts or credentials. Do not
		// echo them into host error output or terminal scrollback.
		return fmt.Errorf("%s interactive command failed: %w", name, err)
	}
	return nil
}
