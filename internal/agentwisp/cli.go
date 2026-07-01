package agentwisp

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func Execute(args []string, in io.Reader, out, errOut io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	project := sanitize(filepath.Base(cwd))
	if project == "" {
		project = "workspace"
	}
	name := containerPrefix + project
	legacyName := legacyContainerPrefix + project

	command := "list"
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "internal-monitor-proxy":
		if os.Getenv("AGENTWISP_INTERNAL_MONITOR_PROXY") != "1" {
			return fmt.Errorf("internal command")
		}
		return runMonitorProxyCommand(args[1:], out, errOut)
	case "add", "start", "run":
		resolved, err := resolveContainerName(name, legacyName)
		if err != nil {
			return err
		}
		return openWorkspace(resolved, cwd, out, errOut)
	case "list", "ls", ".":
		return chooseWorkspace(in, out, errOut)
	case "shell":
		resolved, err := resolveContainerName(name, legacyName)
		if err != nil {
			return err
		}
		return execInteractive(errOut, "container", monitorExecArgs(resolved, out)...)
	case "stop":
		resolved, err := resolveContainerName(name, legacyName)
		if err != nil {
			return err
		}
		return execInteractive(errOut, "container", "stop", resolved)
	case "rm":
		resolved, err := resolveContainerName(name, legacyName)
		if err != nil {
			return err
		}
		return execInteractive(errOut, "container", "rm", resolved)
	case "monitor":
		resolved, err := resolveContainerName(name, legacyName)
		if err != nil {
			return err
		}
		return handleMonitorCommand(args[1:], resolved, out)
	case "help", "-h", "--help":
		printUsage(out)
		return nil
	default:
		printUsage(out)
		return fmt.Errorf("unknown command %q", command)
	}
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, "agentwisp - manage development workspaces")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  agentwisp              Open the workspace manager")
	fmt.Fprintln(out, "  agentwisp add          Add/start the current directory and enter it")
	fmt.Fprintln(out, "  agentwisp start        Start the current directory and enter it")
	fmt.Fprintln(out, "  agentwisp shell        Enter the current directory workspace")
	fmt.Fprintln(out, "  agentwisp stop         Stop the current directory workspace")
	fmt.Fprintln(out, "  agentwisp rm           Remove the current directory workspace")
	fmt.Fprintln(out, "  agentwisp monitor on   Enable metadata-only traffic logging")
	fmt.Fprintln(out, "  agentwisp monitor on --allow-private")
	fmt.Fprintln(out, "  agentwisp monitor off  Disable traffic logging")
	fmt.Fprintln(out, "  agentwisp monitor logs Show traffic logs for the current workspace")
	fmt.Fprintln(out, "  agentwisp monitor clear Clear traffic logs for the current workspace")
	fmt.Fprintln(out, "  agentwisp help         Show this help")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Aliases:")
	fmt.Fprintln(out, "  run = start, list/ls/. = workspace manager")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Workspace manager keys:")
	fmt.Fprintln(out, "  Up/Down or J/K        Move selection")
	fmt.Fprintln(out, "  Enter                 Attach to running or start stopped workspace")
	fmt.Fprintln(out, "  S                     Stop selected workspace")
	fmt.Fprintln(out, "  R                     Refresh workspace statuses")
	fmt.Fprintln(out, "  Q or Esc              Quit")
}
