package agentwisp

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"path"
	"strings"
)

// IDs identify AgentWisp objects, not runtime containers or provider conversations.
type WorkspaceID string
type AgentSessionID string
type AttachmentID string

// Workspace describes the environment only. Running is an observation of the
// container, never evidence that an agent is working. Inventory errors must be
// returned to the caller, not converted into Running=false.
type Workspace struct {
	ID      WorkspaceID
	Name    string // Existing runtime container name, including legacy names.
	Path    string // Host project path; "-" means unavailable in legacy inventory.
	Running bool
	Layout  int // Container layout version label; 0 means created before labels.
	// Readonly lists protected project paths mounted read-only (layout 3+).
	Readonly []string
}

// discoveredWorkspaceID gives inventory a repeatable identity before CORE-05's
// registry exists. It is local to the Apple runtime on this host. Recreating a
// container with the same name reuses this ID; it is NOT an incarnation ID or an
// authorization token. The registry must preserve these IDs during import.
func discoveredWorkspaceID(containerName string) WorkspaceID {
	hash := sha256.Sum256([]byte("apple-container\x00" + containerName))
	return WorkspaceID(fmt.Sprintf("ws_%x", hash))
}

type AgentProvider string

const (
	ProviderCodex  AgentProvider = "codex"
	ProviderClaude AgentProvider = "claude"
)

func (p AgentProvider) valid() bool {
	return p == ProviderCodex || p == ProviderClaude
}

type AgentState string

const (
	AgentUnknown    AgentState = "unknown"
	AgentStarting   AgentState = "starting"
	AgentWorking    AgentState = "working"
	AgentNeedsInput AgentState = "needs-input"
	AgentExited     AgentState = "exited"
)

// AgentSession is one AgentWisp execution. NativeSessionID is optional provider
// conversation metadata; resuming that conversation may create another execution.
// CWD is a POSIX path inside the container, independent of Workspace.Path.
// State may only be updated from agent/process observations, never from inventory.
type AgentSession struct {
	ID              AgentSessionID
	WorkspaceID     WorkspaceID
	Provider        AgentProvider
	NativeSessionID string
	CWD             string
	State           AgentState
}

// NewAgentSession allocates metadata only: it does not start an agent or promise
// persistence. Unknown is intentional until execution has been observed.
func NewAgentSession(workspaceID WorkspaceID, provider AgentProvider, cwd string) (AgentSession, error) {
	if workspaceID == "" {
		return AgentSession{}, fmt.Errorf("session requires a workspace ID")
	}
	if !provider.valid() {
		return AgentSession{}, fmt.Errorf("unsupported agent %q: use claude or codex", provider)
	}
	if !path.IsAbs(cwd) || strings.ContainsRune(cwd, '\x00') {
		return AgentSession{}, fmt.Errorf("session cwd must be an absolute container path without NUL")
	}
	return AgentSession{
		ID: AgentSessionID("ses_" + rand.Text()), WorkspaceID: workspaceID,
		Provider: provider, CWD: cwd, State: AgentUnknown,
	}, nil
}

type AttachmentClient string

const (
	ClientCLI AttachmentClient = "cli"
	ClientWeb AttachmentClient = "web"
)

// Attachment is a view of one execution. Several views may reference the same
// session. Removing a view must not terminate the session or delete its workspace.
// Input ownership will be granted by the broker, not by constructing this object.
type Attachment struct {
	ID        AttachmentID
	SessionID AgentSessionID
	Client    AttachmentClient
	ReadOnly  bool
}

func NewAttachment(sessionID AgentSessionID, client AttachmentClient) (Attachment, error) {
	if sessionID == "" {
		return Attachment{}, fmt.Errorf("attachment requires a session ID")
	}
	if client != ClientCLI && client != ClientWeb {
		return Attachment{}, fmt.Errorf("unsupported attachment client %q", client)
	}
	return Attachment{
		ID: AttachmentID("att_" + rand.Text()), SessionID: sessionID,
		Client: client, ReadOnly: true,
	}, nil
}
