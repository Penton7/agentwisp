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
