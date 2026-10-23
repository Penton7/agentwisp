package agentwisp

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMonitorTokenNotInShellArguments(t *testing.T) {
	cfg := monitorConfig{Token: "test-secret", Host: "192.0.2.1", Port: 1234}
	if strings.Contains(strings.Join(monitoredCommand(cfg, []string{"bash"}), " "), cfg.Token) {
		t.Fatal("secret in command arguments")
	}
	path, cleanup, err := monitorEnvFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("insecure env file: %v %v", info, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "AGENTWISP_MONITOR_TOKEN=test-secret") {
		t.Fatal("token missing")
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("env file not removed")
	}
	cfg.Token = "bad\nINJECTED=value"
	if _, _, err := monitorEnvFile(cfg); err == nil {
		t.Fatal("env injection accepted")
	}
}

func TestMonitorConfigAtomicWriteDoesNotFollowFileSymlink(t *testing.T) {
	t.Setenv("AGENTWISP_STATE_DIR", t.TempDir())
	name := "agentwisp-work-test"
	if err := ensureMonitorStateDir(name); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, monitorConfigPath(name)); err != nil {
		t.Fatal(err)
	}
	if err := saveMonitorConfig(name, monitorConfig{Token: "token"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "unchanged" {
		t.Fatal("followed config symlink")
	}
	info, err := os.Lstat(monitorConfigPath(name))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("bad config permissions: %v %v", info, err)
	}
}

func TestMonitorStateRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTWISP_STATE_DIR", filepath.Join(root, "state"))
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "state")); err != nil {
		t.Fatal(err)
	}
	if err := saveMonitorConfig("agentwisp-work-test", monitorConfig{}); err == nil {
		t.Fatal("symlink state accepted")
	}
}

func TestRemovePreservesStateOnProxyShutdownFailure(t *testing.T) {
	calls := fakeContainer(t, []Workspace{{Name: "agentwisp-work-test"}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	cfg := monitorConfig{Enabled: true, Token: "token", Port: p, PID: 123}
	if err := saveMonitorConfig("agentwisp-work-test", cfg); err != nil {
		t.Fatal(err)
	}
	if err := defaultWorkspaceService().Remove("agentwisp-work-test", true); err == nil {
		t.Fatal("shutdown error ignored")
	}
	got, err := loadMonitorConfig("agentwisp-work-test")
	if err != nil || !reflect.DeepEqual(got, cfg) {
		t.Fatalf("state lost: %+v %v", got, err)
	}
	data, _ := os.ReadFile(calls)
	if strings.Contains(string(data), "rm ") {
		t.Fatalf("removed despite shutdown failure: %s", data)
	}
}

func TestMonitorControlDoesNotFollowRedirect(t *testing.T) {
	var seen atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen.Store(true); w.WriteHeader(http.StatusNoContent) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	_, port, _ := net.SplitHostPort(redirect.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	if monitorProxyHealthy(monitorConfig{Token: "token", Port: p}) {
		t.Fatal("redirect accepted as health")
	}
	if seen.Load() {
		t.Fatal("control token sent to redirect destination")
	}
}

func TestMonitorControlCustomBind(t *testing.T) {
	if got := monitorControlURL(monitorConfig{ListenHost: "192.0.2.1", Port: 1234}, monitorHealthPath); got != "http://192.0.2.1:1234/agentwisp-health" {
		t.Fatal(got)
	}
	r := httptest.NewRequest(http.MethodGet, "http://192.0.2.1:1234/agentwisp-health", nil)
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 1234}))
	if !isInternalMonitorRequest(r, monitorHealthPath) {
		t.Fatal("custom bind health rejected")
	}
}

func TestStripAllConnectionHeaderValues(t *testing.T) {
	h := http.Header{"Connection": []string{"X-First", "X-Second"}, "X-First": []string{"a"}, "X-Second": []string{"b"}}
	removeHopByHopHeaders(h)
	if len(h) != 0 {
		t.Fatalf("hop-by-hop values left: %v", h)
	}
}

func TestBufferedTunnelPreservesReadAhead(t *testing.T) {
	c := &bufferedConn{reader: bufio.NewReader(strings.NewReader("pipelined TLS bytes"))}
	data, err := io.ReadAll(c)
	if err != nil || string(data) != "pipelined TLS bytes" {
		t.Fatalf("lost buffered bytes: %q %v", data, err)
	}
}

func TestProxyRejectsBroadcastAndMappedLocalIP(t *testing.T) {
	for _, input := range []string{"255.255.255.255", "::ffff:127.0.0.1", "224.0.0.1", "::"} {
		if !blockedProxyIP(net.ParseIP(input), true) {
			t.Fatalf("allowed %s", input)
		}
	}
}

func TestTUIEscapesUntrustedDataAndKeepsTinyWidths(t *testing.T) {
	for _, width := range []int{2, 10, 20, 40, 80, 140} {
		var out bytes.Buffer
		items := []Workspace{{Name: "agentwisp-work-x", Path: "/path/\x1b]52;c;evil\a\n\t项目"}}
		renderTUI(&out, items, 0, "error\r\n\x1b[2J", width)
		if strings.Contains(out.String(), "\x1b]52") || strings.Contains(out.String(), "error\r") {
			t.Fatal("terminal escape injection")
		}
		for _, line := range strings.Split(out.String(), "\n") {
			if visibleWidth(line) >= width {
				t.Fatalf("width %d overflow: %q", width, line)
			}
		}
	}
}

func TestDeletionPromptExplainsDataLoss(t *testing.T) {
	var out bytes.Buffer
	renderDeleteConfirmation(&out, Workspace{Name: "agentwisp-work-test", Path: "/code/test"}, 80)
	for _, wanted := range []string{"LOST", "KEPT", "Y: delete", "any other key: cancel"} {
		if !strings.Contains(out.String(), wanted) {
			t.Fatalf("missing %q", wanted)
		}
	}
}

func TestTrafficLogRotationAndClear(t *testing.T) {
	t.Setenv("AGENTWISP_STATE_DIR", t.TempDir())
	name := "agentwisp-work-test"
	if err := ensureMonitorStateDir(name); err != nil {
		t.Fatal(err)
	}
	path := monitorLogPath(name)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(monitorMaxLogBytes); err != nil {
		t.Fatal(err)
	}
	file.Close()
	p := &trafficProxy{logPath: path}
	p.log(trafficEvent{Host: "example.test"})
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > 1024 || info.Mode().Perm() != 0o600 {
		t.Fatalf("rotation failed: %v %v", info, err)
	}
	if err := clearMonitorLogs(name); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(path)
	if info.Size() != 0 {
		t.Fatal("active log not cleared")
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatal("backup not cleared")
	}
}

func TestTrafficLogDoesNotFollowSymlink(t *testing.T) {
	t.Setenv("AGENTWISP_STATE_DIR", t.TempDir())
	name := "agentwisp-work-test"
	if err := ensureMonitorStateDir(name); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, monitorLogPath(name)); err != nil {
		t.Fatal(err)
	}
	p := &trafficProxy{logPath: monitorLogPath(name)}
	p.log(trafficEvent{Host: "example.test"})
	if err := clearMonitorLogs(name); err == nil {
		t.Fatal("clear followed symlink")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "keep" {
		t.Fatal("log modified symlink target")
	}
}

func TestProxyCapacityStillAllowsHealth(t *testing.T) {
	p := &trafficProxy{token: "token", slots: make(chan struct{}, 1)}
	p.slots <- struct{}{}
	for _, target := range []string{"http://example.test", "http://127.0.0.1" + monitorHealthPath} {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.Header.Set("Proxy-Authorization", basicProxyAuthorization("token"))
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		want := http.StatusServiceUnavailable
		if strings.Contains(target, monitorHealthPath) {
			want = http.StatusNoContent
		}
		if w.Code != want {
			t.Fatalf("%s: %d want %d", target, w.Code, want)
		}
	}
}
