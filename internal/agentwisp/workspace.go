package agentwisp

import (
	"fmt"
	"io"
)

func openWorkspace(name, cwd string, out, errOut io.Writer) error {
	exists, err := containerExists(name)
	if err != nil {
		return err
	}

	if !exists {
		return execInteractive(errOut, "container", "run", "-it",
			"--name", name,
			"--label", managedLabel+"=true",
			"--label", workspaceLabel+"="+cwd,
			"--volume", cwd+":/workspace",
			"--workdir", "/workspace",
			"docker.io/library/node:22-bookworm",
			"bash",
		)
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
	return execInteractive(errOut, "container", "exec", "-it", name, "bash")
}

func enterWorkspace(selected workspace, out, errOut io.Writer) error {
	if !selected.Running {
		fmt.Fprintf(out, "Starting %s...\n", selected.Name)
		if err := execInteractive(errOut, "container", "start", selected.Name); err != nil {
			return err
		}
	}
	return execInteractive(errOut, "container", "exec", "-it", selected.Name, "bash")
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
