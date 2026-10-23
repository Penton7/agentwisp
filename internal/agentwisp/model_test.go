package agentwisp

import "testing"

func TestInventoryIdentitySurvivesRefresh(t *testing.T) {
	items := []Workspace{
		{Name: "agentwisp-work-a", Path: "/project", Running: true},
		{Name: "codex-work-a", Path: "/project"},
	}
	fakeContainer(t, items)
	first, err := defaultWorkspaceService().List()
	if err != nil {
		t.Fatal(err)
	}
	second, err := defaultWorkspaceService().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("lost inventory: %v / %v", first, second)
	}
	if first[0].ID == "" || first[0].ID == first[1].ID {
		t.Fatalf("missing or colliding identities: %v", first)
	}
	for i, w := range first {
		if w.ID != second[i].ID || string(w.ID) == w.Name || w.Path != "/project" {
			t.Fatalf("unstable identity or changed legacy mapping: %+v / %+v", w, second[i])
		}
		session, err := NewAgentSession(w.ID, ProviderCodex, "/workspace/chart")
		if err != nil {
			t.Fatal(err)
		}
		if session.State != AgentUnknown || session.NativeSessionID != "" {
			t.Fatalf("container running=%v invented agent state: %+v", w.Running, session)
		}
	}
}

func TestSessionAndAttachmentIdentities(t *testing.T) {
	workspaceID := discoveredWorkspaceID("agentwisp-work-a")
	first, err := NewAgentSession(workspaceID, ProviderClaude, "/workspace/charts/../chart")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewAgentSession(workspaceID, ProviderClaude, "/workspace/chart")
	if err != nil {
		t.Fatal(err)
	}
	// Resuming a provider conversation is a different execution, not another view.
	first.NativeSessionID, second.NativeSessionID = "provider-conversation", "provider-conversation"
	if first.ID == second.ID || first.CWD != "/workspace/charts/../chart" || first.WorkspaceID != second.WorkspaceID {
		t.Fatalf("incorrect session identity/cwd: %+v / %+v", first, second)
	}
	cli, err := NewAttachment(first.ID, ClientCLI)
	if err != nil {
		t.Fatal(err)
	}
	web, err := NewAttachment(first.ID, ClientWeb)
	if err != nil {
		t.Fatal(err)
	}
	if cli.ID == web.ID || cli.SessionID != web.SessionID || !cli.ReadOnly || !web.ReadOnly {
		t.Fatalf("views must be independent and initially read-only: %+v / %+v", cli, web)
	}
	ids := []string{string(workspaceID), string(first.ID), string(second.ID), string(cli.ID), string(web.ID)}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("empty or reused object ID: %q", id)
		}
		seen[id] = true
	}
}

func TestRejectInvalidSessionMetadata(t *testing.T) {
	for _, input := range []struct {
		workspace WorkspaceID
		provider  AgentProvider
		cwd       string
	}{
		{"", ProviderCodex, "/workspace"},
		{"ws_test", "sh", "/workspace"},
		{"ws_test", ProviderClaude, "relative"},
		{"ws_test", ProviderClaude, ""},
		{"ws_test", ProviderClaude, "/workspace\x00bad"},
	} {
		if _, err := NewAgentSession(input.workspace, input.provider, input.cwd); err == nil {
			t.Fatalf("accepted invalid session: %+v", input)
		}
	}
	if _, err := NewAttachment("", ClientCLI); err == nil {
		t.Fatal("accepted attachment without session")
	}
	if _, err := NewAttachment("ses_test", "unrecognized"); err == nil {
		t.Fatal("accepted unknown client")
	}
}
