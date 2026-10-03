package serversetup

// Integration tests against a real VPS. Skipped unless FT_TEST_SSH_HOST is set:
//
//	FT_TEST_SSH_HOST=1.2.3.4 FT_TEST_SSH_PASS=... [FT_TEST_SSH_USER=root] [FT_TEST_SSH_PORT=22] \
//	FT_TEST_MUTATE=1 go test ./desktop/backend/serversetup/ -run Integration -v
//
// Tests that install/uninstall software on the server additionally require
// FT_TEST_MUTATE=1, so pointing this at a machine by accident cannot change it.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func integrationConfig(t *testing.T) SSHConfig {
	t.Helper()
	host := os.Getenv("FT_TEST_SSH_HOST")
	if host == "" {
		t.Skip("FT_TEST_SSH_HOST not set")
	}
	port := 22
	if p := os.Getenv("FT_TEST_SSH_PORT"); p != "" {
		port, _ = strconv.Atoi(p)
	}
	user := os.Getenv("FT_TEST_SSH_USER")
	if user == "" {
		user = "root"
	}
	cfg := SSHConfig{IP: host, Port: port, Username: user, Password: os.Getenv("FT_TEST_SSH_PASS"), AuthType: AuthPassword}
	mode, fp, err := DetectRootMode(cfg)
	if err != nil {
		t.Fatalf("DetectRootMode: %v", err)
	}
	cfg.RootMode, cfg.HostFingerprint = mode, fp
	t.Cleanup(CloseConnections)
	return cfg
}

func requireMutate(t *testing.T) {
	t.Helper()
	if os.Getenv("FT_TEST_MUTATE") != "1" {
		t.Skip("FT_TEST_MUTATE=1 required: this test installs/removes software on the server")
	}
}

// chaosProxy forwards TCP to target and can cut every connection or refuse new
// ones, to stand in for a flaky network path between the app and the VPS.
type chaosProxy struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	conns  []net.Conn
	down   atomic.Bool
}

func newChaosProxy(t *testing.T, target string) *chaosProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &chaosProxy{ln: ln, target: target}
	go p.loop()
	t.Cleanup(func() { ln.Close(); p.cutAll() })
	return p
}

func (p *chaosProxy) loop() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		if p.down.Load() {
			c.Close()
			continue
		}
		up, err := net.DialTimeout("tcp", p.target, 5*time.Second)
		if err != nil {
			c.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, c, up)
		p.mu.Unlock()
		go func() { copyAndClose(up, c) }()
		go func() { copyAndClose(c, up) }()
	}
}

func copyAndClose(dst, src net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	dst.Close()
	src.Close()
}

func (p *chaosProxy) cutAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
}

func (p *chaosProxy) hostPort() (string, int) {
	h, ps, _ := net.SplitHostPort(p.ln.Addr().String())
	port, _ := strconv.Atoi(ps)
	return h, port
}

// The heart of the fix: a long-running command must complete even though the SSH
// connection is destroyed in the middle of it and stays unreachable for a while.
func TestIntegrationDetachedJobSurvivesDisconnect(t *testing.T) {
	real := integrationConfig(t)
	proxy := newChaosProxy(t, net.JoinHostPort(real.IP, strconv.Itoa(real.Port)))
	cfg := real
	cfg.IP, cfg.Port = proxy.hostPort()

	oldPoll, oldRetry := pollInterval, retryUnit
	pollInterval, retryUnit = 500*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { pollInterval, retryUnit = oldPoll, oldRetry })

	script := "for i in 1 2 3 4 5 6 7 8; do echo \"step $i\" >> \"$FT_PROGRESS_FILE\"; sleep 2; done\n" +
		"printf '{\"proto\":2,\"result\":\"ok\",\"data\":{\"args\":\"%s\"},\"logs\":[]}\\n' \"$*\"\n"

	var mu sync.Mutex
	var lines []string
	go func() {
		time.Sleep(5 * time.Second)
		proxy.down.Store(true)
		proxy.cutAll() // the connection dies mid-job...
		time.Sleep(6 * time.Second)
		proxy.down.Store(false) // ...and the path comes back later
	}()

	start := time.Now()
	resp, _, err := runDetached(cfg, script, []string{"hello", "--x=1"}, func(l string) {
		mu.Lock()
		lines = append(lines, l)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("runDetached: %v", err)
	}
	if !resp.IsOK() || !strings.Contains(string(resp.Data), "hello --x=1") {
		t.Fatalf("bad envelope: %+v", resp)
	}
	t.Logf("job finished in %s despite a 6s outage", time.Since(start).Round(time.Second))

	// Every progress line exactly once, in order, none lost across the outage.
	mu.Lock()
	defer mu.Unlock()
	var want []string
	for i := 1; i <= 8; i++ {
		want = append(want, fmt.Sprintf("step %d", i))
	}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("progress = %v\nwant       %v", lines, want)
	}

	res, err := exec(real, `ls /var/tmp | grep -c '^ftjob-' || true`, "")
	if err != nil || strings.TrimSpace(res.output) != "0" {
		t.Fatalf("job directory not cleaned up: %q %v", res.output, err)
	}
}

// The user's original symptom: control commands failing "every other time".
func TestIntegrationProbeIsReliable(t *testing.T) {
	cfg := integrationConfig(t)
	failures := 0
	for i := 0; i < 25; i++ {
		if _, err := Probe(cfg); err != nil {
			failures++
			t.Errorf("probe #%d: %v", i, err)
		}
	}
	if failures > 0 {
		t.Fatalf("%d/25 probes failed", failures)
	}
}

func hexN(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Full deploy flow end to end, then teardown.
func TestIntegrationFullProvision(t *testing.T) {
	requireMutate(t)
	cfg := integrationConfig(t)
	progress := func(prefix string) func(string) {
		return func(l string) { t.Logf("%s | %s", prefix, l) }
	}

	if _, err := Uninstall(cfg, true, false, progress("reset")); err != nil {
		t.Fatalf("initial Uninstall (reset): %v", err)
	}
	probe, err := Probe(cfg)
	if err != nil || probe.Installed {
		t.Fatalf("after reset: probe=%+v err=%v", probe, err)
	}

	inst, err := Install(cfg, progress("install"))
	if err != nil || inst.Version == "" {
		t.Fatalf("Install: %+v %v", inst, err)
	}
	wgPort := 51000 + int(time.Now().UnixNano()%900)
	wg, err := WgSetup(cfg, wgPort, "127.0.0.1:9000", progress("wg"))
	if err != nil || wg.ClientConf == "" || wg.Port != wgPort {
		t.Fatalf("WgSetup: %+v %v", wg, err)
	}
	listenPort := 56000 + int(time.Now().UnixNano()%900)
	if err := Start(cfg, StartOptions{
		Listen:     fmt.Sprintf("0.0.0.0:%d", listenPort),
		Connect:    fmt.Sprintf("127.0.0.1:%d", wg.Port),
		ObfProfile: "rtpopus3", ObfKey: hexN(32), ClientID: hexN(16),
	}, progress("start")); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Independent verification on the server itself, not via our own protocol.
	check := func(cmd, want string) {
		t.Helper()
		res, err := exec(cfg, cmd, "")
		if err != nil || !strings.Contains(res.output, want) {
			t.Fatalf("%s -> %q (err %v), want it to contain %q", cmd, res.output, err, want)
		}
	}
	check("systemctl is-active free-turn-proxy", "active")
	check("ss -lun | grep -c ':"+strconv.Itoa(listenPort)+" '", "1")
	check("wg show ft-wg0 listen-port", strconv.Itoa(wg.Port))

	probe, err = Probe(cfg)
	if err != nil || !probe.Installed || !probe.Running || probe.WgPort != wg.Port {
		t.Fatalf("Probe after deploy: %+v %v", probe, err)
	}
	logs, err := Logs(cfg, 20)
	if err != nil || len(logs) == 0 {
		t.Fatalf("Logs: %v %v", logs, err)
	}
	if err := Stop(cfg); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	check("systemctl is-active free-turn-proxy || true", "inactive")

	if _, err := Uninstall(cfg, true, false, progress("teardown")); err != nil {
		t.Fatalf("final Uninstall: %v", err)
	}
	probe, err = Probe(cfg)
	if err != nil || probe.Installed || probe.WgPort != 0 {
		t.Fatalf("after teardown: %+v %v", probe, err)
	}
}

// When the VPS cannot reach GitHub, the binary must come from this machine.
func TestIntegrationInstallFallsBackToUpload(t *testing.T) {
	requireMutate(t)
	cfg := integrationConfig(t)

	if _, err := Uninstall(cfg, false, false, nil); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := exec(cfg, `cp -n /etc/hosts /etc/hosts.fttest && printf '127.0.0.1 github.com api.github.com\n' >> /etc/hosts`, ""); err != nil {
		t.Fatalf("blackholing GitHub: %v", err)
	}
	t.Cleanup(func() { _, _ = exec(cfg, `[ -f /etc/hosts.fttest ] && mv -f /etc/hosts.fttest /etc/hosts`, "") })

	var mu sync.Mutex
	var lines []string
	began := time.Now()
	inst, err := Install(cfg, func(l string) { mu.Lock(); lines = append(lines, l); t.Logf("%6.1fs  %s", time.Since(began).Seconds(), l); mu.Unlock() })
	if err != nil {
		t.Fatalf("Install with GitHub blocked on the server: %v", err)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"скачиваю на этом компьютере", "Загружаю на сервер", "installing uploaded"} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress lacks %q:\n%s", want, joined)
		}
	}
	if !strings.HasPrefix(inst.Version, "v") {
		t.Errorf("version = %q, want a release tag", inst.Version)
	}
	res, _ := exec(cfg, "/opt/free-turn-proxy/server-linux-amd64 -h 2>&1 | head -n1; ls /var/tmp | grep -c ft-upload || true", "")
	t.Logf("binary check: %s", strings.TrimSpace(res.output))

	if _, err := Uninstall(cfg, false, false, nil); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

// The whole user-management lifecycle against a real server: create a user, list
// them, re-issue their config, watch a real WireGuard handshake show up as "last
// seen", remove them, and confirm the proxy allowlist entry went too.
func TestIntegrationPeerLifecycle(t *testing.T) {
	requireMutate(t)
	cfg := integrationConfig(t)

	if _, err := Uninstall(cfg, true, false, nil); err != nil {
		t.Fatalf("reset: %v", err)
	}
	t.Cleanup(func() { _, _ = Uninstall(cfg, true, false, nil) })

	if _, err := Install(cfg, nil); err != nil {
		t.Fatalf("Install: %v", err)
	}
	wgPort := 51000 + int(time.Now().UnixNano()%900)
	if _, err := WgSetup(cfg, wgPort, "127.0.0.1:9000", nil); err != nil {
		t.Fatalf("WgSetup: %v", err)
	}
	ownerCID := hexN(16)
	if err := Start(cfg, StartOptions{
		Listen: fmt.Sprintf("0.0.0.0:%d", 56000+int(time.Now().UnixNano()%900)), Connect: fmt.Sprintf("127.0.0.1:%d", wgPort),
		ObfProfile: "rtpopus3", ObfKey: hexN(32), ClientID: ownerCID,
	}, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	sh := func(cmd string) string {
		t.Helper()
		res, err := exec(cfg, cmd, "")
		if err != nil {
			t.Fatalf("%s: %v (%s)", cmd, err, res.output)
		}
		return strings.TrimSpace(res.output)
	}
	clients := func() string {
		return sh("CLIENTS_FILE=/opt/free-turn-proxy/clients.json /opt/free-turn-proxy/server-linux-amd64 clients list 2>&1")
	}

	// 1. Fresh server: only the owner, and the server's clock is usable.
	list, err := ShareList(cfg)
	if err != nil || len(list.Peers) != 1 || !list.Peers[0].IsOwner {
		t.Fatalf("fresh server should list just the owner: %+v %v", list, err)
	}
	if skew := time.Now().Unix() - list.ServerNow; skew > 300 || skew < -300 {
		t.Fatalf("server clock reading is off by %ds", skew)
	}
	ownerPub := list.Peers[0].PublicKey

	// 2. Create a user (unicode name on purpose).
	guestCID := hexN(16)
	issued, err := PeerAdd(cfg, "Вася 🙂", "127.0.0.1:9000", guestCID)
	if err != nil {
		t.Fatalf("PeerAdd: %v", err)
	}
	if issued.PublicKey == "" || issued.ClientID != guestCID || !strings.Contains(issued.Conf, "PrivateKey") {
		t.Fatalf("PeerAdd result: %+v", issued)
	}
	if !strings.Contains(clients(), guestCID) {
		t.Fatalf("new user's id is not in the proxy allowlist:\n%s", clients())
	}

	// 3. Listed with the right name, never connected yet.
	list, err = ShareList(cfg)
	if err != nil || len(list.Peers) != 2 {
		t.Fatalf("after add: %+v %v", list, err)
	}
	var guest Peer
	for _, p := range list.Peers {
		if p.PublicKey == issued.PublicKey {
			guest = p
		}
	}
	if guest.Name != "Вася 🙂" || !guest.HasConf || guest.IsOwner || guest.LastHandshake != 0 {
		t.Fatalf("new user as listed: %+v", guest)
	}

	// 4. Sharing again returns the very same connection.
	again, err := PeerConf(cfg, issued.PublicKey, "", "")
	if err != nil || again.Conf != issued.Conf || again.ClientID != guestCID {
		t.Fatalf("re-issued conf differs: %+v %v", again, err)
	}

	// 5. A real client connects: bring up a WireGuard interface with the user's
	// key against this server's own ft-wg0 over loopback. Only a /32 to the
	// server's tunnel address is routed - nothing that could touch the SSH path.
	var priv, serverPub string
	for _, line := range strings.Split(issued.Conf, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "PrivateKey = "); ok {
			priv = v
		}
		if v, ok := strings.CutPrefix(line, "PublicKey = "); ok {
			serverPub = v
		}
	}
	if priv == "" || serverPub == "" {
		t.Fatalf("cannot read keys from conf:\n%s", issued.Conf)
	}
	t.Cleanup(func() { _, _ = exec(cfg, "ip link del ftguest0 2>/dev/null; rm -f /tmp/ftguest.key", "") })
	sh(fmt.Sprintf("umask 077; echo '%s' > /tmp/ftguest.key; ip link add ftguest0 type wireguard && "+
		"wg set ftguest0 private-key /tmp/ftguest.key peer '%s' endpoint 127.0.0.1:%d allowed-ips 10.13.13.1/32 persistent-keepalive 1 && "+
		"ip addr add %s/32 dev ftguest0 && ip link set ftguest0 up && echo up", priv, serverPub, wgPort, issued.IP))

	var seen Peer
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		list, err = ShareList(cfg)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range list.Peers {
			if p.PublicKey == issued.PublicKey {
				seen = p
			}
		}
		if seen.LastHandshake != 0 {
			break
		}
		time.Sleep(time.Second)
	}
	if seen.LastHandshake == 0 {
		t.Fatalf("a real handshake never showed up as 'last seen':\n%s", sh("wg show ft-wg0"))
	}
	if ago := list.ServerNow - seen.LastHandshake; ago < 0 || ago > 30 {
		t.Fatalf("last seen is %ds ago, want a few seconds", ago)
	}
	t.Logf("user shows as last connected %ds ago (rx %d, tx %d bytes)", list.ServerNow-seen.LastHandshake, seen.Rx, seen.Tx)

	// 6. The owner cannot be removed.
	if err := PeerRemove(cfg, ownerPub); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("removing the owner must be refused, got %v", err)
	}

	// 7. Remove the user: peer, stored config and proxy access all go; repeating is harmless.
	sh("ip link del ftguest0")
	if err := PeerRemove(cfg, issued.PublicKey); err != nil {
		t.Fatalf("PeerRemove: %v", err)
	}
	if err := PeerRemove(cfg, issued.PublicKey); err != nil {
		t.Fatalf("second PeerRemove must be a no-op, got %v", err)
	}
	list, err = ShareList(cfg)
	if err != nil || len(list.Peers) != 1 || !list.Peers[0].IsOwner {
		t.Fatalf("after remove only the owner should remain: %+v %v", list, err)
	}
	if strings.Contains(clients(), guestCID) {
		t.Fatalf("removed user still has proxy access:\n%s", clients())
	}
	if strings.Contains(sh("wg show ft-wg0 peers"), issued.PublicKey) {
		t.Fatal("removed user's peer still present in the live WireGuard interface")
	}
	if n := sh("ls /opt/free-turn-proxy/share 2>/dev/null | grep -c conf || true"); n != "0" {
		t.Fatalf("removed user's stored conf left behind (%s files)", n)
	}
}
