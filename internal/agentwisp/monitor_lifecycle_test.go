package agentwisp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The proxy is started by re-executing os.Executable(). Under `go test` that is
// this test binary, so route the internal command before the testing package.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "internal-monitor-proxy" {
		if err := Execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestMonitorLockSerializesLifecycle(t *testing.T) {
	t.Setenv("AGENTWISP_STATE_DIR", t.TempDir())
	previous := monitorLockTimeout
	monitorLockTimeout = 100 * time.Millisecond
	defer func() { monitorLockTimeout = previous }()

	unlock, err := lockMonitorState("agentwisp-work-lock")
	if err != nil {
		t.Fatal(err)
	}
	// flock belongs to the open file description, so a second open in this
	// process contends exactly like another agentwisp process would.
	if _, err := lockMonitorState("agentwisp-work-lock"); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("second holder acquired lock: %v", err)
	}
	if _, err := lockMonitorState("agentwisp-work-other"); err != nil {
		t.Fatalf("lock is not per workspace: %v", err)
	}
	unlock()
	again, err := lockMonitorState("agentwisp-work-lock")
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestConcurrentLaunchesStartOneProxy(t *testing.T) {
	t.Setenv("AGENTWISP_STATE_DIR", t.TempDir())
	t.Setenv("AGENTWISP_PROXY_LISTEN_HOST", "127.0.0.1")
	const name = "agentwisp-work-concurrent"
	cfg := monitorConfig{Enabled: true, Token: "concurrency-token"}
	if err := saveMonitorConfig(name, cfg); err != nil {
		t.Fatal(err)
	}
	monitor := workspaceMonitor{}
	results := make([]monitorConfig, 4)
	errs := make([]error, len(results))
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = monitor.ensureProxy(name)
		}()
	}
	wg.Wait()
	defer func() {
		if err := disableMonitor(name); err != nil {
			t.Error(err)
		}
	}()
	for i := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if results[i].PID != results[0].PID || results[i].Port != results[0].Port {
			t.Fatalf("several proxies started: %+v", results)
		}
	}
	if !monitorProxyHealthy(results[0]) {
		t.Fatal("proxy started from inherited listener is not healthy")
	}
}

func TestMonitorOnDefersWithoutWorkspaceNetwork(t *testing.T) {
	t.Setenv("AGENTWISP_STATE_DIR", t.TempDir())
	t.Setenv("AGENTWISP_PROXY_LISTEN_HOST", "")
	stopped := func(string) ([]net.IP, error) { return nil, nil }
	cfg, err := enableMonitor("agentwisp-work-stopped", monitorOnOptions{}, stopped)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := loadMonitorConfig("agentwisp-work-stopped")
	if cfg.Port != 0 || cfg.PID != 0 || !saved.Enabled {
		t.Fatalf("expected deferred start: %+v %+v", cfg, saved)
	}
}

func TestHostAddressForContainerNeverWildcard(t *testing.T) {
	_, bridge, _ := net.ParseCIDR("192.168.64.1/24")
	bridge.IP = net.ParseIP("192.168.64.1").To4()
	_, wifi, _ := net.ParseCIDR("10.0.0.5/24")
	wifi.IP = net.ParseIP("10.0.0.5").To4()
	loopback := &net.IPNet{IP: net.ParseIP("127.0.0.1").To4(), Mask: net.CIDRMask(8, 32)}
	host := []net.Addr{loopback, wifi, bridge}

	got, err := hostAddressForContainer([]net.IP{net.ParseIP("192.168.64.7")}, host)
	if err != nil || got != "192.168.64.1" {
		t.Fatalf("got %q %v", got, err)
	}
	// A spoofed loopback or foreign address must not select any interface.
	for _, ips := range [][]net.IP{{net.ParseIP("127.0.0.1")}, {net.ParseIP("203.0.113.9")}, nil} {
		if got, err := hostAddressForContainer(ips, host); err == nil {
			t.Fatalf("%v selected %q", ips, got)
		}
	}
}

func TestContainerAddressesFromInspect(t *testing.T) {
	data := []byte(`[{"configuration":{"id":"agentwisp-work-a","labels":{"dev.agentwisp.workspace":"/10.1.1.1"}},
		"networks":[{"network":"default","address":"192.168.64.7/24","gateway":"192.168.64.1","hostname":"x"}]}]`)
	ips, err := containerAddressesFromInspect(data)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(ips)
	if !strings.Contains(got, "192.168.64.7") || !strings.Contains(got, "192.168.64.1") || strings.Contains(got, "10.1.1.1") {
		t.Fatalf("unexpected addresses %s", got)
	}
}

func TestHTTPProxyLogsChunkedRequestBytes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	logPath := filepath.Join(t.TempDir(), "traffic.ndjson")
	proxy := &trafficProxy{workspace: "agentwisp-work-bytes", logPath: logPath, transport: http.DefaultTransport.(*http.Transport).Clone()}

	body := strings.Repeat("x", 1000)
	request := httptest.NewRequest(http.MethodPost, upstream.URL+"/upload", io.NopCloser(strings.NewReader(body)))
	request.ContentLength = -1 // as the server reports for Transfer-Encoding: chunked
	recorder := httptest.NewRecorder()
	proxy.handleHTTP(recorder, request)

	file, err := os.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatal("no traffic event")
	}
	var event trafficEvent
	if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.BytesIn != int64(len(body)) || event.BytesOut != 2 || event.Decision != "allow" {
		t.Fatalf("unexpected event %+v", event)
	}
}

func TestAllowlistPatterns(t *testing.T) {
	options, err := parseMonitorOnOptions([]string{"--allow=GitHub.com., *.npmjs.org", "--allow=github.com,192.0.2.7"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"github.com", "*.npmjs.org", "192.0.2.7"}
	if !slices.Equal(options.AllowHosts, want) {
		t.Fatalf("got %q", options.AllowHosts)
	}
	for _, bad := range []string{"--allow=*", "--allow=*.com", "--allow=a..b", "--allow=", "--allow=-x.com", "--allow", "--allow=evil.com/path"} {
		if _, err := parseMonitorOnOptions([]string{bad}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	for host, allowed := range map[string]bool{
		"github.com:443": true, "GITHUB.COM.": true, "api.github.com:443": false,
		"registry.npmjs.org": true, "npmjs.org": false, "evilnpmjs.org": false,
		"192.0.2.7:80": true, "[2001:db8::1]:443": false, "github.com.evil.io": false,
	} {
		if got := hostAllowed(host, want); got != allowed {
			t.Errorf("%s: got %v", host, got)
		}
	}
	if !hostAllowed("anything.example", nil) {
		t.Fatal("empty allowlist must keep monitor-only behavior")
	}
}

func TestProxyDeniesHostsOutsideAllowlist(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "traffic.ndjson")
	dialed := false
	proxy := &trafficProxy{workspace: "agentwisp-work-allow", logPath: logPath, allowHosts: []string{"example.com"},
		transport: roundTripFunc(func(*http.Request) (*http.Response, error) { dialed = true; return nil, io.EOF })}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "http://other.test/", nil),
		httptest.NewRequest(http.MethodConnect, "https://other.test:443", nil),
	} {
		if request.Method == http.MethodConnect {
			request.Host = "other.test:443"
		}
		recorder := httptest.NewRecorder()
		if request.Method == http.MethodConnect {
			proxy.handleConnect(recorder, request)
		} else {
			proxy.handleHTTP(recorder, request)
		}
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s: status %d", request.Method, recorder.Code)
		}
	}
	data, _ := os.ReadFile(logPath)
	if dialed || strings.Count(string(data), "not in allowlist") != 2 {
		t.Fatalf("dialed=%v log=%s", dialed, data)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
