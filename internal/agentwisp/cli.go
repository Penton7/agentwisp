package agentwisp

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func Execute(args []string, in io.Reader, out, errOut io.Writer) error {
	return executeWithService(defaultWorkspaceService(), args, in, out, errOut)
}

func executeWithService(service *WorkspaceService, args []string, in io.Reader, out, errOut io.Writer) error {
	command := "list"
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "list", "ls", ".":
		if len(args) <= 1 {
			return chooseWorkspace(service, in, out, errOut)
		}
		if len(args) != 2 || args[1] != "--json" {
			return fmt.Errorf("usage: agw list [--json]")
		}
		return writeWorkspaceListJSON(service, out)
	case "inspect":
		id, err := inspectJSONArgs(args[1:])
		if err != nil {
			return err
		}
		return writeWorkspaceInspectJSON(service, id, out)
	case "help", "-h", "--help":
		printUsage(out)
		return nil
	case "watch":
		notify := true
		for _, arg := range args[1:] {
			if arg != "--no-notify" {
				return fmt.Errorf("usage: agw watch [--no-notify]")
			}
			notify = false
		}
		stop := make(chan struct{})
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(interrupts)
		go func() { <-interrupts; close(stop) }()
		fmt.Fprintln(out, "Watching agent sessions started with `agw agent`; Ctrl-C stops.")
		return service.Watch(notify, out, stop)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return err
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
		force, yes, purge := false, false, false
		for _, arg := range args[1:] {
			switch arg {
			case "--force", "-f":
				force = true
			case "--yes", "-y":
				yes = true
			case "--purge":
				purge = true
			default:
				return fmt.Errorf("unknown rm option %q", arg)
			}
		}
		resolved, err := service.Resolve(cwd)
		if err != nil {
			return err
		}
		if !yes {
			fmt.Fprintf(out, "Remove %s? Container files will be lost; host project files, traffic logs and saved agent logins are kept.\nType the container name to confirm: ", resolved)
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
		if purge {
			if err := PurgeState(resolved); err != nil {
				return fmt.Errorf("removed %s, but purging saved agent logins failed: %w", resolved, err)
			}
			fmt.Fprintf(out, "Removed %s and its saved agent logins. Host project files and traffic logs kept.\n", resolved)
			return nil
		}
		fmt.Fprintf(out, "Removed %s. Host project files, traffic logs and saved agent logins kept (--purge deletes logins).\n", resolved)
		return nil
	case "task":
		return handleTaskCommand(service, args[1:], cwd, in, out)
	case "monitor":
		resolved, err := service.Resolve(cwd)
		if err != nil {
			return err
		}
		return handleMonitorCommand(args[1:], resolved, service.runtime.Addresses, out)
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
	fmt.Fprintln(out, "  agw list --json        List workspaces as JSON")
	fmt.Fprintln(out, "  agw inspect <id> --json  Inspect a workspace by its full ID")
	fmt.Fprintln(out, "  agentwisp add          Add/start the current directory and enter it")
	fmt.Fprintln(out, "  agentwisp start        Start the current directory and enter it")
	fmt.Fprintln(out, "  agentwisp shell        Enter the current directory workspace")
	fmt.Fprintln(out, "  agw agent claude [-- args...]  Run Claude Code in the current workspace")
	fmt.Fprintln(out, "  agw agent codex [-- args...]   Run Codex in the current workspace")
	fmt.Fprintln(out, "  agw task new <name> [--from <ref>]  Clone the repo into ../<repo>-<name> with its own workspace")
	fmt.Fprintln(out, "  agw task list          List tasks of the current repository")
	fmt.Fprintln(out, "  agw task fetch <name>  Bring branch agw/<name> back as a git bundle")
	fmt.Fprintln(out, "  agw task rm <name> [--yes] [--force]  Remove task clone and workspace")
	fmt.Fprintln(out, "  agw watch [--no-notify]  Notify when an agent needs input or finishes a turn")
	fmt.Fprintln(out, "  agentwisp stop         Stop the current directory workspace")
	fmt.Fprintln(out, "  agentwisp rm [--force] [--yes] [--purge]  Remove current workspace (prompts by default)")
	fmt.Fprintln(out, "                        --force allows running; --yes skips confirmation;")
	fmt.Fprintln(out, "                        --purge also deletes saved agent logins")
	fmt.Fprintln(out, "  agentwisp monitor on   Enable metadata-only traffic logging")
	fmt.Fprintln(out, "  agentwisp monitor on --allow-private")
	fmt.Fprintln(out, "  agentwisp monitor on --allow=github.com,*.npmjs.org  Forward only these hosts")
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
