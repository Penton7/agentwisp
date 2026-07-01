package agentwisp

import (
	"regexp"
	"strings"
)

const (
	containerPrefix       = "agentwisp-work-"
	workspaceLabel        = "dev.agentwisp.workspace"
	managedLabel          = "dev.agentwisp.managed"
	legacyContainerPrefix = "codex-work-"
	legacyWorkspaceLabel  = "dev.codexbox.workspace"
)

type workspace struct {
	Name    string
	Path    string
	Running bool
}

func sanitize(s string) string {
	s = strings.ToLower(s)
	re := regexp.MustCompile(`[^a-z0-9-]+`)
	s = re.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
