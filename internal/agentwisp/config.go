package agentwisp

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var unsafeNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func workspaceContainerName(path string) string {
	project := sanitize(filepath.Base(path))
	if project == "" {
		project = "workspace"
	}
	if len(project) > 40 {
		project = project[:40]
	}
	hash := sha256.Sum256([]byte(filepath.Clean(path)))
	return fmt.Sprintf("%s%s-%x", containerPrefix, project, hash[:8])
}

const (
	containerPrefix       = "agentwisp-work-"
	workspaceLabel        = "dev.agentwisp.workspace"
	managedLabel          = "dev.agentwisp.managed"
	legacyContainerPrefix = "codex-work-"
	legacyWorkspaceLabel  = "dev.codexbox.workspace"
)

func sanitize(s string) string {
	s = strings.ToLower(s)
	s = unsafeNameChars.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
