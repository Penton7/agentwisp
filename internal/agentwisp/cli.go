package agentwisp

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func Execute(args []string, in io.Reader, out, errOut io.Writer) error {
	return executeWithService(defaultWorkspaceService(), args, in, out, errOut)
}

func executeWithService(service *WorkspaceService, args []string, in io.Reader, out, errOut io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return err
	}

	command := "list"
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "agent":
		launch, err := agentCommand(args[1:])
		if err != nil {
			return err
		}
		resolved, err := service.Resolve(cwd)
		if err != nil {
			return err
		}
		return service.RunAgent(resolved, launch, out, errOut)
	case "internal-monitor-proxy":
		if os.Getenv("AGENTWISP_INTERNAL_MONITOR_PROXY") != "1" {
			return fmt.Errorf("internal command")
		}
		return runMonitorProxyCommand(args[1:], out, errOut)
	case "add", "start", "run":
		resolved, err := service.Resolve(cwd)
		if err != nil {
			return err
		}
		return service.Open(resolved, cwd, out, errOut)
	case "list", "ls", ".":
		return chooseWorkspace(service, in, out, errOut)
	case "shell":
		resolved, err := service.Resolve(cwd)
		if err != nil {
			return err
		}
		return service.Shell(resolved, errOut)
	case "stop":
		resolved, err := service.Resolve(cwd)
		if err != nil {
			return err
		}
		return service.Stop(resolved)
	case "rm", "remove":
		force, yes := false, false
		for _, arg := range args[1:] {
			switch arg {
			case "--force", "-f":
				force = true
			case "--yes", "-y":
				yes = true
			default:
				return fmt.Errorf("unknown rm option %q", arg)
			}
		}
		resolved, err := service.Resolve(cwd)
		if err != nil {
			return err
		}
		if !yes {
			fmt.Fprintf(out, "Remove %s? Container files and credentials will be lost; host project files and traffic logs are kept.\nType the container name to confirm: ", resolved)
			line, err := bufio.NewReader(in).ReadString('\n')
			if err != nil && err != io.EOF {
				return err
			}
			if strings.TrimSpace(line) != resolved {
				fmt.Fprintln(out, "Removal cancelled.")
				return nil
			}
		}
		if err := service.Remove(resolved, force); err != nil {
			return err
		}
		fmt.Fprintf(out, "Removed %s. Host project files and traffic logs kept.\n", resolved)
		return nil
	case "monitor":
		resolved, err := service.Resolve(cwd)
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
	fmt.Fprintln(out, "Also available as agw (same commands and workspace state).")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  agentwisp              Open the workspace manager")
	fmt.Fprintln(out, "  agentwisp add          Add/start the current directory and enter it")
	fmt.Fprintln(out, "  agentwisp start        Start the current directory and enter it")
	fmt.Fprintln(out, "  agentwisp shell        Enter the current directory workspace")
	fmt.Fprintln(out, "  agw agent claude [-- args...]  Run Claude Code in the current workspace")
	fmt.Fprintln(out, "  agw agent codex [-- args...]   Run Codex in the current workspace")
	fmt.Fprintln(out, "  agentwisp stop         Stop the current directory workspace")
	fmt.Fprintln(out, "  agentwisp rm [--force] [--yes]  Remove current workspace (prompts by default)")
	fmt.Fprintln(out, "                        --force allows running; --yes skips confirmation")
	fmt.Fprintln(out, "  agentwisp monitor on   Enable metadata-only traffic logging")
	fmt.Fprintln(out, "  agentwisp monitor on --allow-private")
	fmt.Fprintln(out, "  agentwisp monitor off  Disable traffic logging")
	fmt.Fprintln(out, "  agentwisp monitor logs Show traffic logs for the current workspace")
	fmt.Fprintln(out, "  agentwisp monitor clear Clear traffic logs for the current workspace")
	fmt.Fprintln(out, "  agentwisp help         Show this help")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Aliases:")
	fmt.Fprintln(out, "  run = start, remove = rm, list/ls/. = workspace manager")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Environment:")
	fmt.Fprintf(out, "  AGENTWISP_IMAGE       Container image for new workspaces (default: %s)\n", defaultWorkspaceImage)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Workspace manager keys:")
	fmt.Fprintln(out, "  Up/Down or J/K        Move selection")
	fmt.Fprintln(out, "  Enter                 Attach to running or start stopped workspace")
	fmt.Fprintln(out, "  S                     Stop selected workspace")
	fmt.Fprintln(out, "  D                     Delete selected workspace with confirmation")
	fmt.Fprintln(out, "  R                     Refresh workspace statuses")
	fmt.Fprintln(out, "  Q or Esc              Quit")
}
