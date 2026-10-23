package agentwisp

import (
	"fmt"
	"io"
	"os"
)

func agentCommand(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: agw agent <claude|codex> [-- agent-arguments...]")
	}
	if !AgentProvider(args[0]).valid() {
		return nil, fmt.Errorf("unsupported agent %q: use claude or codex", args[0])
	}
	rest := args[1:]
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	return append([]string{args[0]}, rest...), nil
}

func (s *WorkspaceService) RunAgent(name string, command []string, out, errOut io.Writer) error {
	if len(command) == 0 || !AgentProvider(command[0]).valid() {
		return fmt.Errorf("unsupported agent: use claude or codex")
	}
	if err := s.require(name); err != nil {
		return err
	}
	if err := s.startLocked(name, out); err != nil {
		return err
	}
	if err := s.runtime.CheckAgent(name, command[0]); err != nil {
		return fmt.Errorf("cannot find or execute %s in workspace %s; use an image containing the agent: %w", command[0], name, err)
	}
	session, err := NewAgentSession(discoveredWorkspaceID(name), AgentProvider(command[0]), "/workspace")
	if err != nil {
		return err
	}
	hooked, err := withStatusHooks(command)
	if err != nil {
		return err
	}
	// Status is best effort and must never prevent the agent from starting.
	_ = writeHostAgentStatus(name, session, "starting")
	defer os.Remove(agentStatusPath(name, session))
	launch := append([]string{"env", "AGENTWISP_SESSION=" + string(session.ID)}, hooked...)
	return s.launch(RuntimeLaunch{Name: name, Command: launch}, errOut)
}
