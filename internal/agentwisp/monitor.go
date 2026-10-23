package agentwisp

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	monitorConfigFile     = "monitor.json"
	monitorLogFile        = "traffic.ndjson"
	monitorLockFile       = "monitor.lock"
	monitorProxyLogFile   = "proxy.log"
	monitorAuthUser       = "agentwisp"
	monitorHealthPath     = "/agentwisp-health"
	monitorStopPath       = "/agentwisp-shutdown"
	monitorControlTimeout = 150 * time.Millisecond
	monitorMaxLogBytes    = 8 << 20
	monitorMaxRequests    = 64
	monitorTunnelLifetime = 30 * time.Minute
)

type monitorConfig struct {
	Enabled      bool   `json:"enabled"`
	ListenHost   string `json:"listen_host,omitempty"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	PID          int    `json:"pid"`
	Token        string `json:"token"`
	AllowPrivate bool   `json:"allow_private"`
}

// Variable so tests can exercise contention without long waits.
var monitorLockTimeout = 10 * time.Second

var monitorControlClient = http.Client{
	Timeout:       monitorControlTimeout,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		Proxy: nil,
	},
}

// addressLookup reports a running container's network addresses. It is used
// only to pick which host interface the proxy listens on.
type addressLookup func(string) ([]net.IP, error)

var errMonitorNoNetwork = errors.New("workspace has no reachable network address; start it first")

type trafficEvent struct {
	Time      string `json:"time"`
	Workspace string `json:"workspace"`
	Scheme    string `json:"scheme"`
	Host      string `json:"host"`
	Port      string `json:"port"`
	Method    string `json:"method"`
	BytesIn   int64  `json:"bytes_in"`
	BytesOut  int64  `json:"bytes_out"`
	Decision  string `json:"decision"`
	Error     string `json:"error,omitempty"`
}

func handleMonitorCommand(args []string, workspaceName string, addresses addressLookup, out io.Writer) error {
	if len(args) == 0 {
		return printMonitorStatus(workspaceName, out)
	}

	switch args[0] {
	case "on", "enable":
		options, err := parseMonitorOnOptions(args[1:])
		if err != nil {
			return err
		}
		cfg, err := enableMonitor(workspaceName, options, addresses)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Traffic monitor enabled for %s\n", workspaceName)
		if cfg.Port == 0 {
			fmt.Fprintln(out, "Proxy: starts with the next agw shell/agent (workspace is not running)")
		} else {
			fmt.Fprintf(out, "Proxy: %s\n", proxyDescription(cfg))
		}
		fmt.Fprintf(out, "Private LAN: %s\n", privateLANStatus(cfg))
		fmt.Fprintln(out, "Logs: "+monitorLogPath(workspaceName))
		fmt.Fprintln(out, "Note: existing shells need to be reopened through agentwisp to receive proxy env.")
		return nil
	case "off", "disable":
		if err := disableMonitor(workspaceName); err != nil {
			return err
		}
		fmt.Fprintf(out, "Traffic monitor disabled for %s\n", workspaceName)
		return nil
	case "logs", "log":
		return printMonitorLogs(workspaceName, out)
	case "clear":
		if err := clearMonitorLogs(workspaceName); err != nil {
			return err
		}
		fmt.Fprintf(out, "Traffic logs cleared for %s\n", workspaceName)
		return nil
	default:
		return fmt.Errorf("unknown monitor command %q", args[0])
	}
}

func printMonitorStatus(workspaceName string, out io.Writer) error {
	cfg, err := loadMonitorConfig(workspaceName)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		fmt.Fprintf(out, "Traffic monitor is off for %s\n", workspaceName)
		return nil
	}
	fmt.Fprintf(out, "Traffic monitor is on for %s\n", workspaceName)
	fmt.Fprintf(out, "Proxy: %s\n", proxyDescription(cfg))
	fmt.Fprintf(out, "Private LAN: %s\n", privateLANStatus(cfg))
	fmt.Fprintf(out, "Blocks: %s\n", monitorBlockedDestinations(cfg))
	fmt.Fprintf(out, "PID: %d\n", cfg.PID)
	fmt.Fprintln(out, "Logs: "+monitorLogPath(workspaceName))
	return nil
}

func monitorTUIStatus(workspaces []Workspace, selected int) string {
	if selected < 0 || selected >= len(workspaces) {
		return ""
	}
	name := workspaces[selected].Name
	cfg, err := loadMonitorConfig(name)
	if err != nil || !cfg.Enabled {
		return "PROXY off"
	}

	health := "down"
	if monitorProxyHealthy(cfg) {
		health = "up"
	}
	return fmt.Sprintf("PROXY on  health=%s  pid=%d  port=%d  private=%s  log=%s",
		health,
		cfg.PID,
		cfg.Port,
		privateLANStatus(cfg),
		monitorLogPath(name),
	)
}

type monitorOnOptions struct {
	AllowPrivate bool
}

func parseMonitorOnOptions(args []string) (monitorOnOptions, error) {
	var options monitorOnOptions
	for _, arg := range args {
		switch arg {
		case "--allow-private":
			options.AllowPrivate = true
		default:
			return monitorOnOptions{}, fmt.Errorf("unknown monitor on option %q", arg)
		}
	}
	return options, nil
}

// lockMonitorState serializes monitor lifecycle for one workspace across
// processes, so two shells opened at once cannot start two proxies. Only the
// public lifecycle entry points lock; *Locked helpers expect the lock held.
func lockMonitorState(workspaceName string) (func(), error) {
	if err := ensureMonitorStateDir(workspaceName); err != nil {
		return nil, err
	}
	path := filepath.Join(monitorStateDir(workspaceName), monitorLockFile)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	fd := int(file.Fd())
	for deadline := time.Now().Add(monitorLockTimeout); ; {
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(fd, syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			file.Close()
			return nil, fmt.Errorf("lock monitor state: %w", err)
		}
		if time.Now().After(deadline) {
			file.Close()
			return nil, fmt.Errorf("monitor state for %s is busy in another agentwisp process", workspaceName)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func enableMonitor(workspaceName string, options monitorOnOptions, addresses addressLookup) (monitorConfig, error) {
	unlock, err := lockMonitorState(workspaceName)
	if err != nil {
		return monitorConfig{}, err
	}
	defer unlock()
	cfg, err := loadMonitorConfig(workspaceName)
	if err != nil {
		return monitorConfig{}, err
	}
	restart := cfg.Enabled && cfg.AllowPrivate != options.AllowPrivate
	cfg.Enabled = true
	cfg.AllowPrivate = options.AllowPrivate
	if cfg.Token == "" {
		token, err := randomMonitorToken()
		if err != nil {
			return monitorConfig{}, err
		}
		cfg.Token = token
	}
	if restart {
		if err := shutdownMonitorProxy(cfg); err != nil {
			return monitorConfig{}, err
		}
		cfg.PID = 0
		cfg.Port = 0
	}
	if err := saveMonitorConfig(workspaceName, cfg); err != nil {
		return monitorConfig{}, err
	}
	started, err := ensureMonitorProxyLocked(workspaceName, addresses)
	if errors.Is(err, errMonitorNoNetwork) {
		// Enabled is persisted; the proxy starts with the next launch.
		return cfg, nil
	}
	return started, err
}

func disableMonitor(workspaceName string) error {
	unlock, err := lockMonitorState(workspaceName)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := loadMonitorConfig(workspaceName)
	if err != nil {
		return err
	}
	if !cfg.Enabled && cfg.Port == 0 {
		return nil
	}
	if err := shutdownMonitorProxy(cfg); err != nil {
		return err
	}
	cfg.Enabled = false
	cfg.PID = 0
	cfg.Port = 0
	return saveMonitorConfig(workspaceName, cfg)
}

func printMonitorLogs(workspaceName string, out io.Writer) error {
	file, err := os.Open(monitorLogPath(workspaceName))
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(out, "No traffic logs for %s\n", workspaceName)
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fmt.Fprintln(out, scanner.Text())
	}
	return scanner.Err()
}

func clearMonitorLogs(workspaceName string) error {
	if err := ensureMonitorStateDir(workspaceName); err != nil {
		return err
	}
	file, err := os.OpenFile(monitorLogPath(workspaceName), os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if err := os.Remove(monitorLogPath(workspaceName) + ".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Prepare keeps proxy credentials out of host process arguments. The caller
// owns cleanup and must keep the env file alive through the runtime launch.
type workspaceMonitor struct {
	addresses addressLookup
}

func (workspaceMonitor) Disable(name string) error { return disableMonitor(name) }
func (m workspaceMonitor) Prepare(req RuntimeLaunch) (RuntimeLaunch, func(), error) {
	cleanup := func() {}
	cfg, err := loadMonitorConfig(req.Name)
	if err != nil {
		return req, cleanup, fmt.Errorf("load monitor config: %w", err)
	}
	if cfg.Enabled {
		cfg, err = m.ensureProxy(req.Name)
		if err != nil {
			return req, cleanup, fmt.Errorf("start monitor: %w", err)
		}
		envPath, release, err := monitorEnvFile(cfg)
		if err != nil {
			return req, cleanup, err
		}
		req.EnvFile = envPath
		req.Command = monitoredCommand(cfg, req.Command)
		cleanup = release
	}
	return req, cleanup, nil
}

func (m workspaceMonitor) ensureProxy(name string) (monitorConfig, error) {
	unlock, err := lockMonitorState(name)
	if err != nil {
		return monitorConfig{}, err
	}
	defer unlock()
	return ensureMonitorProxyLocked(name, m.addresses)
}

func monitoredCommand(_ monitorConfig, launch []string) []string {
	// sh -c consumes one argument as $0; the rest stay distinct in "$@".
	script := monitorShellPrelude() + `; exec "$@"`
	return append([]string{"sh", "-c", script, "agw-agent"}, launch...)
}

func monitorEnvFile(cfg monitorConfig) (string, func(), error) {
	if strings.ContainsAny(cfg.Host+cfg.ListenHost+cfg.Token, "\r\n") || cfg.Port < 1 || cfg.Port > 65535 {
		return "", nil, fmt.Errorf("invalid monitor environment")
	}
	file, err := os.CreateTemp("", "agentwisp-proxy-*.env")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	_, err = fmt.Fprintf(file, "AGENTWISP_MONITOR_TOKEN=%s\nAGENTWISP_MONITOR_PORT=%d\nAGENTWISP_PROXY_HOST=%s\n", cfg.Token, cfg.Port, containerProxyHost(cfg))
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return file.Name(), cleanup, nil
}

func monitorEnv(cfg monitorConfig) []string {
	host := cfg.Host
	if host == "" {
		host = "auto-gateway"
	}
	proxyURL := monitorProxyURL(host, cfg)
	noProxy := "localhost,127.0.0.1,::1"
	return []string{
		"HTTP_PROXY=" + proxyURL,
		"HTTPS_PROXY=" + proxyURL,
		"http_proxy=" + proxyURL,
		"https_proxy=" + proxyURL,
		"NO_PROXY=" + noProxy,
		"no_proxy=" + noProxy,
		"AGENTWISP_TRAFFIC_MONITOR=metadata",
	}
}

// containerProxyHost is the address a workspace uses to reach the proxy. An
// explicit AGENTWISP_PROXY_HOST wins; a concrete bridge bind address is reachable
// as-is; otherwise the in-container script detects the default gateway.
func containerProxyHost(cfg monitorConfig) string {
	if cfg.Host != "" {
		return cfg.Host
	}
	if ip := net.ParseIP(cfg.ListenHost); ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
		return cfg.ListenHost
	}
	return ""
}

// monitorShellPrelude exports proxy variables; callers append the final exec.
func monitorShellPrelude() string {
	return strings.Join([]string{
		`proxy_host="${AGENTWISP_PROXY_HOST:-}"`,
		`if [ -z "$proxy_host" ] && command -v ip >/dev/null 2>&1; then proxy_host="$(ip route show default 2>/dev/null | awk '{print $3; exit}')"; fi`,
		`if [ -z "$proxy_host" ] && [ -r /proc/net/route ]; then gw_hex="$(awk '$2 == "00000000" {print $3; exit}' /proc/net/route)"; if [ -n "$gw_hex" ]; then b1="$(printf '%s' "$gw_hex" | cut -c7-8)"; b2="$(printf '%s' "$gw_hex" | cut -c5-6)"; b3="$(printf '%s' "$gw_hex" | cut -c3-4)"; b4="$(printf '%s' "$gw_hex" | cut -c1-2)"; proxy_host="$(printf '%d.%d.%d.%d' "0x$b1" "0x$b2" "0x$b3" "0x$b4")"; fi; fi`,
		`if [ -z "$proxy_host" ]; then echo 'agentwisp: cannot detect proxy gateway; set AGENTWISP_PROXY_HOST' >&2; exit 1; fi`,
		`case "$proxy_host" in *:*) proxy_host="[$proxy_host]";; esac`,
		`proxy_url="http://agentwisp:${AGENTWISP_MONITOR_TOKEN}@${proxy_host}:${AGENTWISP_MONITOR_PORT}"`,
		`export HTTP_PROXY="$proxy_url" HTTPS_PROXY="$proxy_url" http_proxy="$proxy_url" https_proxy="$proxy_url"`,
		`export NO_PROXY=localhost,127.0.0.1,::1 no_proxy=localhost,127.0.0.1,::1 AGENTWISP_TRAFFIC_MONITOR=metadata`,
		`unset AGENTWISP_MONITOR_TOKEN AGENTWISP_MONITOR_PORT`,
	}, "; ")
}

func monitorProxyURL(host string, cfg monitorConfig) string {
	return fmt.Sprintf("http://agentwisp:%s@%s:%d", cfg.Token, host, cfg.Port)
}

func proxyDescription(cfg monitorConfig) string {
	host := cfg.Host
	if host == "" {
		host = "<auto-container-gateway>"
	}
	return fmt.Sprintf("http://agentwisp:<token>@%s:%d", host, cfg.Port)
}

func ensureMonitorProxyLocked(workspaceName string, addresses addressLookup) (monitorConfig, error) {
	cfg, err := loadMonitorConfig(workspaceName)
	if err != nil {
		return monitorConfig{}, err
	}
	if !cfg.Enabled {
		return cfg, nil
	}
	restart := false
	if cfg.Token == "" {
		token, err := randomMonitorToken()
		if err != nil {
			return monitorConfig{}, err
		}
		cfg.Token = token
		restart = true
	}
	if !restart && cfg.Port > 0 && monitorProxyHealthy(cfg) {
		return cfg, nil
	}
	if restart {
		_ = shutdownMonitorProxy(cfg)
	}

	listenHost, err := monitorListenHost(workspaceName, addresses)
	if err != nil {
		return monitorConfig{}, err
	}
	// The child serves this exact listener (fd 3), so no other process can
	// take the port between selection and bind.
	listener, err := net.Listen("tcp", net.JoinHostPort(listenHost, "0"))
	if err != nil {
		return monitorConfig{}, err
	}
	defer listener.Close()
	listenerFile, err := listener.(*net.TCPListener).File()
	if err != nil {
		return monitorConfig{}, err
	}
	defer listenerFile.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	cfg.ListenHost = listenHost
	if err := saveMonitorConfig(workspaceName, cfg); err != nil {
		return monitorConfig{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return monitorConfig{}, err
	}
	logPath := filepath.Join(monitorStateDir(workspaceName), monitorProxyLogFile)
	proxyLog, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return monitorConfig{}, err
	}
	defer proxyLog.Close()

	cmd := exec.Command(executable, "internal-monitor-proxy", "--workspace", workspaceName)
	cmd.Env = append(os.Environ(), "AGENTWISP_INTERNAL_MONITOR_PROXY=1")
	cmd.Stdin = nil
	cmd.Stdout = proxyLog
	cmd.Stderr = proxyLog
	cmd.ExtraFiles = []*os.File{listenerFile}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return monitorConfig{}, err
	}

	cfg.Port = port
	cfg.PID = cmd.Process.Pid
	// Retain the child handle until readiness; killing this specific unreaped
	// child on failure cannot target a reused PID from a state file.
	ready := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if monitorProxyHealthy(cfg) {
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return monitorConfig{}, fmt.Errorf("monitor proxy did not become ready; see %s", logPath)
	}
	if err := saveMonitorConfig(workspaceName, cfg); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return monitorConfig{}, err
	}
	if err := cmd.Process.Release(); err != nil {
		return monitorConfig{}, err
	}
	return cfg, nil
}

// monitorListenHost picks the host address the proxy binds to. Without an
// explicit override it is the host's own address on the container network, so
// the proxy is not exposed on Wi-Fi/LAN interfaces. 0.0.0.0 is opt-in only.
func monitorListenHost(workspaceName string, addresses addressLookup) (string, error) {
	if host := os.Getenv("AGENTWISP_PROXY_LISTEN_HOST"); host != "" {
		if net.ParseIP(host) == nil {
			return "", fmt.Errorf("AGENTWISP_PROXY_LISTEN_HOST must be an IP address")
		}
		return host, nil
	}
	if addresses == nil {
		return "", errMonitorNoNetwork
	}
	containerIPs, err := addresses(workspaceName)
	if err != nil {
		return "", fmt.Errorf("find workspace network: %w", err)
	}
	hostAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	return hostAddressForContainer(containerIPs, hostAddrs)
}

// hostAddressForContainer returns the host interface address on the same
// subnet as the container. Container-reported IPs only select among the host's
// own addresses; they are never bound or dialed directly.
func hostAddressForContainer(containerIPs []net.IP, hostAddrs []net.Addr) (string, error) {
	for _, containerIP := range containerIPs {
		if containerIP == nil || containerIP.IsLoopback() || containerIP.IsUnspecified() {
			continue
		}
		for _, addr := range hostAddrs {
			network, ok := addr.(*net.IPNet)
			if !ok || network.IP.To4() == nil || network.IP.IsLoopback() {
				continue
			}
			if network.Contains(containerIP) {
				return network.IP.String(), nil
			}
		}
	}
	if len(containerIPs) == 0 {
		return "", errMonitorNoNetwork
	}
	return "", fmt.Errorf("no host interface shares the workspace network; set AGENTWISP_PROXY_LISTEN_HOST")
}

func runMonitorProxyCommand(args []string, out, errOut io.Writer) error {
	workspaceName := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--workspace" && i+1 < len(args) {
			i++
			workspaceName = args[i]
		}
	}
	if workspaceName == "" {
		return fmt.Errorf("internal monitor proxy requires --workspace")
	}
	// The parent passes its bound listener as the first extra file (fd 3).
	listener, err := net.FileListener(os.NewFile(3, "monitor-listener"))
	if err != nil {
		return fmt.Errorf("internal monitor proxy requires an inherited listener: %w", err)
	}
	defer listener.Close()

	cfg, err := loadMonitorConfig(workspaceName)
	if err != nil {
		return err
	}
	if cfg.Token == "" {
		return fmt.Errorf("monitor proxy token is missing")
	}

	proxy := &trafficProxy{
		workspace:    workspaceName,
		logPath:      monitorLogPath(workspaceName),
		token:        cfg.Token,
		allowPrivate: cfg.AllowPrivate,
		transport:    directHTTPTransport(cfg.AllowPrivate),
		slots:        make(chan struct{}, monitorMaxRequests),
	}
	server := &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	proxy.shutdown = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
	fmt.Fprintf(out, "agentwisp monitor proxy listening on %s\n", listener.Addr())
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(errOut, "agentwisp monitor proxy: %v\n", err)
		return err
	}
	return nil
}

type trafficProxy struct {
	workspace    string
	logPath      string
	token        string
	allowPrivate bool
	transport    http.RoundTripper
	shutdown     func()
	logMu        sync.Mutex
	slots        chan struct{}
}

func (p *trafficProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !proxyAuthorized(r, p.token) {
		start := time.Now()
		w.Header().Set("Proxy-Authenticate", "Basic realm=\"agentwisp\"")
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		p.log(trafficEventFromRequest(r, p.workspace, start, 0, 0, "deny", fmt.Errorf("proxy authentication required")))
		return
	}

	if isInternalMonitorRequest(r, monitorHealthPath) && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if isInternalMonitorRequest(r, monitorStopPath) && r.Method == http.MethodPost {
		w.WriteHeader(http.StatusNoContent)
		if p.shutdown != nil {
			go p.shutdown()
		}
		return
	}
	if p.slots != nil {
		select {
		case p.slots <- struct{}{}:
			defer func() { <-p.slots }()
		default:
			http.Error(w, "proxy capacity reached", http.StatusServiceUnavailable)
			return
		}
	}

	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

func (p *trafficProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if !strings.Contains(target, ":") {
		target += ":443"
	}
	start := time.Now()
	if !allowedConnectPort(target) {
		http.Error(w, "CONNECT port blocked", http.StatusForbidden)
		p.log(trafficEventFromRequest(r, p.workspace, start, 0, 0, "deny", errors.New("blocked CONNECT port")))
		return
	}

	upstream, err := dialProxyTarget(r.Context(), target, p.allowPrivate)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errBlockedProxyDestination) {
			status = http.StatusForbidden
		}
		http.Error(w, "connect failed", status)
		p.log(trafficEventFromRequest(r, p.workspace, start, 0, 0, "deny", err))
		return
	}
	defer upstream.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		p.log(trafficEventFromRequest(r, p.workspace, start, 0, 0, "deny", fmt.Errorf("hijacking unsupported")))
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		p.log(trafficEventFromRequest(r, p.workspace, start, 0, 0, "deny", err))
		return
	}
	defer client.Close()
	deadline := time.Now().Add(monitorTunnelLifetime)
	_ = client.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		p.log(trafficEventFromRequest(r, p.workspace, start, 0, 0, "deny", err))
		return
	}

	clientCounter := &countingConn{Conn: client}
	upstreamCounter := &countingConn{Conn: upstream}
	done := make(chan struct{}, 2)
	go copyTunnel(upstreamCounter, &bufferedConn{Conn: clientCounter, reader: buffered.Reader}, done)
	go copyTunnel(clientCounter, upstreamCounter, done)
	for i := 0; i < 2; i++ {
		<-done
	}

	p.log(trafficEventFromRequest(r, p.workspace, start, upstreamCounter.written, clientCounter.written, "allow", nil))
}

func (p *trafficProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	if outReq.URL.Scheme == "" {
		outReq.URL.Scheme = "http"
	}
	if outReq.URL.Host == "" {
		outReq.URL.Host = r.Host
	}
	if outReq.URL.Scheme != "http" {
		http.Error(w, "unsupported proxy scheme", http.StatusBadRequest)
		p.log(trafficEventFromRequest(r, p.workspace, start, 0, 0, "deny", errors.New("unsupported proxy scheme")))
		return
	}
	removeHopByHopHeaders(outReq.Header)
	// ContentLength is -1 for chunked bodies; log bytes actually forwarded.
	requestBody := &countingReadCloser{ReadCloser: http.NoBody}
	if r.Body != nil && r.Body != http.NoBody {
		requestBody.ReadCloser = r.Body
		outReq.Body = requestBody
	}

	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errBlockedProxyDestination) {
			status = http.StatusForbidden
		}
		http.Error(w, "proxy failed", status)
		p.log(trafficEventFromRequest(r, p.workspace, start, requestBody.count.Load(), 0, "deny", err))
		return
	}
	defer resp.Body.Close()

	removeHopByHopHeaders(resp.Header)
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	bytesOut, _ := io.Copy(w, resp.Body)
	p.log(trafficEventFromRequest(r, p.workspace, start, requestBody.count.Load(), bytesOut, "allow", nil))
}

func trafficEventFromRequest(r *http.Request, workspace string, start time.Time, bytesIn, bytesOut int64, decision string, err error) trafficEvent {
	host := r.Host
	if r.URL != nil && r.URL.Host != "" {
		host = r.URL.Host
	}
	name, port, splitErr := net.SplitHostPort(host)
	if splitErr != nil {
		name = host
		if r.Method == http.MethodConnect || r.URL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	scheme := "http"
	if r.Method == http.MethodConnect {
		scheme = "https"
	} else if r.URL != nil && r.URL.Scheme != "" {
		scheme = r.URL.Scheme
	}

	event := trafficEvent{
		Time:      start.UTC().Format(time.RFC3339),
		Workspace: workspace,
		Scheme:    scheme,
		Host:      name,
		Port:      port,
		Method:    r.Method,
		BytesIn:   bytesIn,
		BytesOut:  bytesOut,
		Decision:  decision,
	}
	if err != nil {
		event.Error = err.Error()
	}
	return event
}

func (p *trafficProxy) log(event trafficEvent) {
	p.logMu.Lock()
	defer p.logMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(p.logPath), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	data = append(data, '\n')
	if info, err := os.Lstat(p.logPath); err == nil {
		if !info.Mode().IsRegular() {
			return
		}
		if info.Size()+int64(len(data)) > monitorMaxLogBytes {
			if err := os.Rename(p.logPath, p.logPath+".1"); err != nil {
				return
			}
		}
	}
	file, err := os.OpenFile(p.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return
	}
	_, _ = file.Write(data)
}

// Hijack can return bytes already read after the CONNECT headers.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(data []byte) (int, error) { return c.reader.Read(data) }

// countingReadCloser is read by the transport's writer goroutine.
type countingReadCloser struct {
	io.ReadCloser
	count atomic.Int64
}

func (c *countingReadCloser) Read(data []byte) (int, error) {
	n, err := c.ReadCloser.Read(data)
	c.count.Add(int64(n))
	return n, err
}

type countingConn struct {
	net.Conn
	written int64
}

func (c *countingConn) Write(data []byte) (int, error) {
	n, err := c.Conn.Write(data)
	c.written += int64(n)
	return n, err
}

func copyTunnel(dst, src net.Conn, done chan<- struct{}) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
	done <- struct{}{}
}

func removeHopByHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, field := range strings.Split(value, ",") {
			if name := strings.TrimSpace(field); name != "" {
				header.Del(name)
			}
		}
	}
	for _, key := range []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Proxy-Connection",
		"TE",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		header.Del(key)
	}
}

func copyHeader(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func directHTTPTransport(allowPrivate bool) http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.MaxConnsPerHost = 16
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialProxyTarget(ctx, address, allowPrivate)
	}
	return transport
}

func loadMonitorConfig(workspaceName string) (monitorConfig, error) {
	data, err := os.ReadFile(monitorConfigPath(workspaceName))
	if errors.Is(err, os.ErrNotExist) {
		return monitorConfig{Host: proxyHostForContainers()}, nil
	}
	if err != nil {
		return monitorConfig{}, err
	}
	var cfg monitorConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return monitorConfig{}, err
	}
	if cfg.Host == "host.docker.internal" {
		cfg.Host = ""
	}
	if cfg.Host == "" {
		cfg.Host = proxyHostForContainers()
	}
	return cfg, nil
}

func saveMonitorConfig(workspaceName string, cfg monitorConfig) error {
	if err := ensureMonitorStateDir(workspaceName); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := monitorConfigPath(workspaceName)
	file, err := os.CreateTemp(filepath.Dir(path), ".monitor-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func ensureMonitorStateDir(workspaceName string) error {
	if !validWorkspaceName(workspaceName) {
		return fmt.Errorf("invalid workspace name %q", workspaceName)
	}
	for _, dir := range []string{agentWispStateDir(), filepath.Join(agentWispStateDir(), "workspaces"), monitorStateDir(workspaceName)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("monitor state must be a real directory: %s", dir)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func monitorConfigPath(workspaceName string) string {
	return filepath.Join(monitorStateDir(workspaceName), monitorConfigFile)
}

func monitorLogPath(workspaceName string) string {
	return filepath.Join(monitorStateDir(workspaceName), monitorLogFile)
}

func monitorStateDir(workspaceName string) string {
	return filepath.Join(agentWispStateDir(), "workspaces", sanitize(workspaceName))
}

func agentWispStateDir() string {
	if custom := os.Getenv("AGENTWISP_STATE_DIR"); custom != "" {
		return custom
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".agentwisp"
	}
	return filepath.Join(home, ".agentwisp")
}

func proxyHostForContainers() string {
	if host := os.Getenv("AGENTWISP_PROXY_HOST"); host != "" {
		return host
	}
	return ""
}

func monitorControlURL(cfg monitorConfig, path string) string {
	host := cfg.ListenHost
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Port)) + path
}

func monitorProxyHealthy(cfg monitorConfig) bool {
	if cfg.Port <= 0 || cfg.Token == "" {
		return false
	}
	request, err := http.NewRequest(http.MethodGet, monitorControlURL(cfg, monitorHealthPath), nil)
	if err != nil {
		return false
	}
	request.Header.Set("Proxy-Authorization", basicProxyAuthorization(cfg.Token))

	response, err := monitorControlClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusNoContent
}

func shutdownMonitorProxy(cfg monitorConfig) error {
	if cfg.Port <= 0 || cfg.Token == "" {
		return nil
	}
	request, err := http.NewRequest(http.MethodPost, monitorControlURL(cfg, monitorStopPath), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Proxy-Authorization", basicProxyAuthorization(cfg.Token))

	response, err := monitorControlClient.Do(request)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return nil
		}
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("monitor proxy shutdown returned HTTP %d", response.StatusCode)
	}
	return nil
}

func basicProxyAuthorization(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(monitorAuthUser+":"+token))
}

var errBlockedProxyDestination = errors.New("blocked local proxy destination")

func blockedProxyDestination(hostport string, allowPrivate bool) (bool, string) {
	_, err := resolveAllowedProxyIPs(context.Background(), hostport, allowPrivate)
	if err == nil {
		return false, ""
	}
	if errors.Is(err, errBlockedProxyDestination) {
		return true, err.Error()
	}
	return false, ""
}

func resolveAllowedProxyIPs(ctx context.Context, hostport string, allowPrivate bool) ([]net.IP, error) {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return nil, fmt.Errorf("%w: empty host", errBlockedProxyDestination)
	}
	ips := make([]net.IP, 0, 1)
	if ip := net.ParseIP(host); ip != nil {
		ips = append(ips, ip)
	} else {
		resolved, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		ips = append(ips, resolved...)
	}
	if len(ips) == 0 {
		return nil, errors.New("proxy destination did not resolve to an IP")
	}
	for _, ip := range ips {
		if blockedProxyIP(ip, allowPrivate) {
			return nil, errBlockedProxyDestination
		}
	}
	return ips, nil
}

func blockedProxyIP(ip net.IP, allowPrivate bool) bool {
	if !allowPrivate && ip.IsPrivate() {
		return true
	}
	return !ip.IsGlobalUnicast() || ip.IsLoopback() ||
		ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast()
}

func dialProxyTarget(ctx context.Context, hostport string, allowPrivate bool) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	ips, err := resolveAllowedProxyIPs(ctx, hostport, allowPrivate)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 15 * time.Second}
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("proxy destination did not connect")
}

func randomMonitorToken() (string, error) {
	data := make([]byte, 24)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func proxyAuthorized(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	header := r.Header.Get("Proxy-Authorization")
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(header, prefix)))
	if err != nil {
		return false
	}
	username, password, ok := strings.Cut(string(decoded), ":")
	if !ok || username != monitorAuthUser {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(password), []byte(token)) == 1
}

func allowedConnectPort(hostport string) bool {
	_, port, err := net.SplitHostPort(hostport)
	return err == nil && port == "443"
}

func isInternalMonitorRequest(r *http.Request, path string) bool {
	if r.URL == nil || r.URL.Path != path {
		return false
	}
	host := r.Host
	if r.URL.Host != "" {
		host = r.URL.Host
	}
	name, _, err := net.SplitHostPort(host)
	if err == nil {
		host = name
	}
	host = strings.Trim(host, "[]")
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return true
	}
	if local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		localHost, _, err := net.SplitHostPort(local.String())
		return err == nil && host == localHost
	}
	return false
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func privateLANStatus(cfg monitorConfig) string {
	if cfg.AllowPrivate {
		return "allowed"
	}
	return "blocked"
}

func monitorBlockedDestinations(cfg monitorConfig) string {
	blocks := []string{"loopback", "link-local", "multicast"}
	if !cfg.AllowPrivate {
		blocks = append(blocks, "private-lan")
	}
	return strings.Join(blocks, ", ")
}
