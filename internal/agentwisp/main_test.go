package agentwisp

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestWorkspacePathFromInspectLabel(t *testing.T) {
	data := []byte(`[
		{
			"configuration": {
				"labels": {
					"dev.agentwisp.workspace": "/Users/me/project"
				}
			}
		}
	]`)

	got, err := workspacePathFromInspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/me/project" {
		t.Fatalf("got %q", got)
	}
}

func TestWorkspacePathFromLegacyInspectLabel(t *testing.T) {
	data := []byte(`{
		"labels": {
			"dev.codexbox.workspace": "/Users/me/legacy-project"
		}
	}`)

	got, err := workspacePathFromInspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/me/legacy-project" {
		t.Fatalf("got %q", got)
	}
}

func TestAgentWispIdentifiers(t *testing.T) {
	if containerPrefix != "agentwisp-work-" {
		t.Fatalf("container prefix: %q", containerPrefix)
	}
	if workspaceLabel != "dev.agentwisp.workspace" {
		t.Fatalf("workspace label: %q", workspaceLabel)
	}
	if managedLabel != "dev.agentwisp.managed" {
		t.Fatalf("managed label: %q", managedLabel)
	}
}

func TestWorkspacePathFromInspectMount(t *testing.T) {
	data := []byte(`{
		"mounts": [
			{
				"type": "bind",
				"source": "/Users/me/legacy-project",
				"destination": "/workspace"
			}
		]
	}`)

	got, err := workspacePathFromInspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/me/legacy-project" {
		t.Fatalf("got %q", got)
	}
}

func TestWorkspacePathFromInspectVolumeString(t *testing.T) {
	data := []byte(`{"volumes":["/Users/me/project:/workspace"]}`)

	got, err := workspacePathFromInspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/me/project" {
		t.Fatalf("got %q", got)
	}
}

func TestPrintWorkspaces(t *testing.T) {
	var out bytes.Buffer
	printWorkspaces(&out, []Workspace{
		{Name: "agentwisp-work-api", Path: "/code/api", Running: true},
		{Name: "agentwisp-work-web", Path: "/code/web", Running: false},
	})

	text := out.String()
	for _, expected := range []string{"STATUS", "running", "stopped", "/code/api", "/code/web"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("output does not contain %q:\n%s", expected, text)
		}
	}
}

func TestReadTUIKey(t *testing.T) {
	tests := []struct {
		input string
		want  tuiKey
	}{
		{input: "\x1b[A", want: keyUp},
		{input: "\x1b[B", want: keyDown},
		{input: "k", want: keyUp},
		{input: "j", want: keyDown},
		{input: "\r", want: keyEnter},
		{input: "r", want: keyRefresh},
		{input: "s", want: keyStop},
		{input: "d", want: keyDelete},
		{input: "Y", want: keyConfirm},
		{input: "q", want: keyQuit},
		{input: "c", want: keyClaude},
		{input: "x", want: keyCodex},
		{input: "f", want: keyFetch},
		{input: "?", want: keyHelp},
	}

	for _, test := range tests {
		got, err := readTUIKey(bufio.NewReader(strings.NewReader(test.input)))
		if err != nil {
			t.Fatalf("read %q: %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("read %q: got %v, want %v", test.input, got, test.want)
		}
	}
}

func TestRenderTUI(t *testing.T) {
	var out bytes.Buffer
	renderTUI(&out, []Workspace{
		{Name: "agentwisp-work-api", Path: "/code/api", Running: true},
		{Name: "agentwisp-work-web", Path: "/code/web", Running: false},
	}, 1, "", 100)

	text := out.String()
	for _, expected := range []string{"agentwisp", "NO AGENT", "STOPPED", "api", "web", "shell", "claude"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("output does not contain %q:\n%s", expected, text)
		}
	}
}

func TestRenderTUICompact(t *testing.T) {
	var out bytes.Buffer
	renderTUI(&out, []Workspace{
		{Name: "agentwisp-work-api", Path: "/a/very/long/path/to/the/workspace", Running: true},
	}, 0, "", 40)

	for _, line := range strings.Split(out.String(), "\n") {
		if visibleWidth(line) > 40 {
			t.Fatalf("line is wider than terminal: %q", line)
		}
	}
}

func TestRenderTUIKeepsLastColumnEmpty(t *testing.T) {
	const terminalWidth = 80

	var out bytes.Buffer
	renderTUI(&out, []Workspace{
		{
			Name:    "agentwisp-work-project-with-a-long-name",
			Path:    "/Users/example/projects/a-workspace-with-a-long-name",
			Running: true,
		},
	}, 0, "", terminalWidth)

	for _, line := range strings.Split(out.String(), "\n") {
		if visibleWidth(line) >= terminalWidth {
			t.Fatalf("line reaches terminal wrap column: width=%d line=%q", visibleWidth(line), line)
		}
	}
}

func TestPrintUsage(t *testing.T) {
	var out bytes.Buffer
	printUsage(&out)

	text := out.String()
	for _, expected := range []string{
		"agentwisp add",
		"agentwisp start",
		"agentwisp help",
		"Workspace manager keys",
		"Stop selected workspace",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("help does not contain %q:\n%s", expected, text)
		}
	}
}

func TestFitCell(t *testing.T) {
	if got := fitCell("api", 6); got != "api   " {
		t.Fatalf("padding: got %q", got)
	}
	if got := fitCell("very-long-name", 8); got != "very-lo…" {
		t.Fatalf("truncation: got %q", got)
	}
}

func TestMonitorEnv(t *testing.T) {
	env := strings.Join(monitorEnv(monitorConfig{Host: "host.test", Port: 49152, Token: "secret"}), "\n")
	for _, expected := range []string{
		"HTTP_PROXY=http://agentwisp:secret@host.test:49152",
		"HTTPS_PROXY=http://agentwisp:secret@host.test:49152",
		"NO_PROXY=localhost,127.0.0.1,::1",
		"AGENTWISP_TRAFFIC_MONITOR=metadata",
	} {
		if !strings.Contains(env, expected) {
			t.Fatalf("monitor env does not contain %q:\n%s", expected, env)
		}
	}
}

func TestMonitorShellScriptAutoDetectsGateway(t *testing.T) {
	script := strings.Join(monitoredCommand(monitorConfig{Port: 49152, Token: "secret"}, []string{"bash"}), " ")
	for _, expected := range []string{
		"ip route show default",
		"/proc/net/route",
		"AGENTWISP_MONITOR_TOKEN",
		"AGENTWISP_MONITOR_PORT",
		`exec "$@"`,
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("monitor shell script does not contain %q:\n%s", expected, script)
		}
	}
	if strings.Contains(script, "host.docker.internal") {
		t.Fatalf("monitor shell script contains docker-specific host:\n%s", script)
	}
}

func TestProxyAuthorization(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("agentwisp:secret")))

	if !proxyAuthorized(request, "secret") {
		t.Fatal("expected proxy request to be authorized")
	}
	if proxyAuthorized(request, "other") {
		t.Fatal("request should not be authorized with a different token")
	}

	request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("other:secret")))
	if proxyAuthorized(request, "secret") {
		t.Fatal("request should not be authorized with a different username")
	}
}

func TestParseMonitorOnOptions(t *testing.T) {
	options, err := parseMonitorOnOptions([]string{"--allow-private"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.AllowPrivate {
		t.Fatal("expected private LAN opt-in")
	}

	if _, err := parseMonitorOnOptions([]string{"--unknown"}); err == nil {
		t.Fatal("expected unknown monitor option to fail")
	}
}

func TestMonitorBlockedDestinationsLabel(t *testing.T) {
	if got := monitorBlockedDestinations(monitorConfig{}); !strings.Contains(got, "private-lan") {
		t.Fatalf("expected private-lan block by default: %q", got)
	}
	if got := monitorBlockedDestinations(monitorConfig{AllowPrivate: true}); strings.Contains(got, "private-lan") {
		t.Fatalf("private-lan should not be listed when allowed: %q", got)
	}
}

func TestBlockedProxyDestination(t *testing.T) {
	for _, target := range []string{
		"127.0.0.1:8080",
		"localhost:8080",
		"[::1]:8080",
		"169.254.169.254:80",
		"10.0.0.1:80",
		"172.16.0.1:80",
		"192.168.1.1:80",
		"[fc00::1]:443",
		"[fe80::1]:443",
	} {
		blocked, _ := blockedProxyDestination(target, false)
		if !blocked {
			t.Fatalf("expected %s to be blocked", target)
		}
	}
}

func TestPrivateProxyDestinationCanBeAllowed(t *testing.T) {
	blocked, reason := blockedProxyDestination("192.168.1.1:80", true)
	if blocked {
		t.Fatalf("private destination should be allowed with explicit opt-in: %s", reason)
	}

	blocked, _ = blockedProxyDestination("127.0.0.1:8080", true)
	if !blocked {
		t.Fatal("loopback destination should stay blocked even when private LAN is allowed")
	}
}

func TestPublicProxyDestinationAllowed(t *testing.T) {
	blocked, reason := blockedProxyDestination("93.184.216.34:80", false)
	if blocked {
		t.Fatalf("public destination should be allowed: %s", reason)
	}
}

func TestAllowedConnectPort(t *testing.T) {
	if !allowedConnectPort("example.com:443") {
		t.Fatal("expected CONNECT 443 to be allowed")
	}
	for _, target := range []string{"example.com:22", "example.com:80", "example.com"} {
		if allowedConnectPort(target) {
			t.Fatalf("expected CONNECT target %q to be blocked", target)
		}
	}
}

func TestRemoveHopByHopHeaders(t *testing.T) {
	header := http.Header{}
	header.Set("Connection", "X-Remove-Me")
	header.Set("X-Remove-Me", "1")
	header.Set("Proxy-Authorization", "secret")
	header.Set("Proxy-Connection", "keep-alive")
	header.Set("Upgrade", "websocket")
	header.Set("X-Keep-Me", "ok")

	removeHopByHopHeaders(header)

	for _, removed := range []string{"Connection", "X-Remove-Me", "Proxy-Authorization", "Proxy-Connection", "Upgrade"} {
		if header.Get(removed) != "" {
			t.Fatalf("expected %s to be removed", removed)
		}
	}
	if header.Get("X-Keep-Me") != "ok" {
		t.Fatal("expected end-to-end header to remain")
	}
}

func TestInternalMonitorRequest(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1234"+monitorHealthPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !isInternalMonitorRequest(request, monitorHealthPath) {
		t.Fatal("expected local health request to be internal")
	}

	request, err = http.NewRequest(http.MethodGet, "http://example.com"+monitorHealthPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if isInternalMonitorRequest(request, monitorHealthPath) {
		t.Fatal("external request path must not be treated as internal monitor request")
	}
}

func TestMonitorStateDirCanBeOverridden(t *testing.T) {
	t.Setenv("AGENTWISP_STATE_DIR", "/tmp/agentwisp-test-state")

	got := monitorStateDir("agentwisp-work-api")
	if !strings.HasPrefix(got, "/tmp/agentwisp-test-state/workspaces/") {
		t.Fatalf("unexpected state dir: %q", got)
	}
}

func TestLoadMonitorConfigDefaultHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTWISP_STATE_DIR", dir)
	t.Setenv("AGENTWISP_PROXY_HOST", "host.test")

	cfg, err := loadMonitorConfig("agentwisp-work-api")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "host.test" {
		t.Fatalf("host: got %q", cfg.Host)
	}
	if _, err := os.Stat(monitorConfigPath("agentwisp-work-api")); !os.IsNotExist(err) {
		t.Fatalf("default load should not create config file: %v", err)
	}
}

func TestLoadMonitorConfigDropsDockerHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTWISP_STATE_DIR", dir)

	if err := os.MkdirAll(monitorStateDir("agentwisp-work-api"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(monitorConfigPath("agentwisp-work-api"), []byte(`{"enabled":true,"host":"host.docker.internal"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadMonitorConfig("agentwisp-work-api")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "" {
		t.Fatalf("docker-specific host should be dropped, got %q", cfg.Host)
	}
}

func visibleWidth(value string) int {
	const (
		plain = iota
		escape
		csi
	)
	state := plain
	width := 0
	for _, char := range value {
		switch state {
		case plain:
			if char == '\x1b' {
				state = escape
			} else {
				width++
			}
		case escape:
			if char == '[' {
				state = csi
			} else {
				state = plain
			}
		case csi:
			if char >= '@' && char <= '~' {
				state = plain
			}
		}
	}
	return width
}
