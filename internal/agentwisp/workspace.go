package agentwisp

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func removeWorkspace(name string, force bool) error {
	if !validWorkspaceName(name) {
		return fmt.Errorf("invalid workspace name %q", name)
	}
	running, err := containerRunning(name)
	if err != nil {
		return err
	}
	if running && !force {
		return fmt.Errorf("workspace %s is running; stop it first or use --force", name)
	}
	// Keep monitor state and logs so a failed shutdown can be retried safely.
	if err := disableMonitor(name); err != nil {
		return fmt.Errorf("stop workspace proxy: %w", err)
	}
	args := []string{"rm"}
	if force {
		args = append(args, "--force")
	}
	_, err = commandOutput("container", append(args, name)...)
	return err
}

func stopWorkspaceAndMonitor(name string) error {
	if err := stopWorkspace(name); err != nil {
		return err
	}
	if err := disableMonitor(name); err != nil {
		return fmt.Errorf("workspace stopped, but proxy shutdown failed: %w", err)
	}
	return nil
}

func workspaceImage() (string, error) {
	image := os.Getenv("AGENTWISP_IMAGE")
	if image == "" {
		return "docker.io/library/node:22-bookworm", nil
	}
	if strings.HasPrefix(image, "-") || strings.ContainsAny(image, " \t\r\n") {
		return "", fmt.Errorf("invalid AGENTWISP_IMAGE")
	}
	return image, nil
}

func openWorkspace(name, cwd string, out, errOut io.Writer) error {
	exists, err := containerExists(name)
	if err != nil {
		return err
	}

	if !exists {
		image, err := workspaceImage()
		if err != nil {
			return err
		}
		if strings.ContainsAny(cwd, ":\r\n") {
			return fmt.Errorf("workspace path contains unsupported volume delimiter")
		}
		args := []string{"run", "-it",
			"--name", name,
			"--label", managedLabel + "=true",
			"--label", workspaceLabel + "=" + cwd,
			"--volume", cwd + ":/workspace",
			"--workdir", "/workspace",
		}
		return execWorkspaceShell(name, args, []string{image}, out, errOut)
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
	return execWorkspaceShell(name, []string{"exec", "-it", "--workdir", "/workspace"}, []string{name}, out, errOut)
}

func enterWorkspace(selected workspace, out, errOut io.Writer) error {
	if !selected.Running {
		fmt.Fprintf(out, "Starting %s...\n", selected.Name)
		if err := execInteractive(errOut, "container", "start", selected.Name); err != nil {
			return err
		}
	}
	return execWorkspaceShell(selected.Name, []string{"exec", "-it", "--workdir", "/workspace"}, []string{selected.Name}, out, errOut)
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
