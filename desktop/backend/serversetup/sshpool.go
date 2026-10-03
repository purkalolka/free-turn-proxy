package serversetup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Timings are vars, not consts, so tests can shrink them.
var (
	dialTimeout      = 15 * time.Second
	handshakeTimeout = 30 * time.Second
	tcpKeepAlive     = 5 * time.Second
	sshKeepAlive     = 5 * time.Second
	// A connection whose peer stops answering keepalives for this long is dead:
	// closing it turns a silent, indefinite hang (a black-holed path never
	// errors on its own) into an error the caller can retry.
	keepaliveReplyTimeout = 20 * time.Second
	// How long an unused connection stays open for the next command.
	poolIdleTimeout = 60 * time.Second
)

// pooledConn is one live SSH connection shared by consecutive commands to the
// same host. Opening a fresh TCP+SSH connection per command (what this package
// did before) costs a full handshake each time and, on hosts that rate-limit
// new logins (ufw "limit ssh", fail2ban, provider firewalls), the burst of
// connections a provisioning run makes is itself what gets the client cut off.
type pooledConn struct {
	key         string
	client      *ssh.Client
	fingerprint string

	dead chan struct{} // closed once the connection is gone
	once sync.Once

	mu      sync.Mutex
	active  int
	closing bool
	idle    *time.Timer
}

var pool = struct {
	sync.Mutex
	m map[string]*pooledConn
}{m: map[string]*pooledConn{}}

// poolKey identifies "the same login": host, port, user and credentials.
func poolKey(cfg SSHConfig) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\x00%s\x00%s", cfg.IP, cfg.Port, cfg.Username, cfg.AuthType, cfg.Password, cfg.SSHKey)
	return hex.EncodeToString(h.Sum(nil))
}

// dialConn opens a new authenticated SSH connection (not yet pooled).
func dialConn(cfg SSHConfig) (*pooledConn, error) {
	authMethods, err := authMethodsFor(cfg)
	if err != nil {
		return nil, err
	}

	var captured string
	clientCfg := &ssh.ClientConfig{
		User: cfg.Username,
		Auth: authMethods,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			captured = fingerprintOf(key)
			if cfg.HostFingerprint != "" && cfg.HostFingerprint != captured {
				return &mitmError{expected: cfg.HostFingerprint, got: captured}
			}
			return nil
		},
		Timeout: dialTimeout,
	}

	addr := net.JoinHostPort(cfg.IP, strconv.Itoa(cfg.Port))
	dialer := net.Dialer{Timeout: dialTimeout, KeepAlive: tcpKeepAlive}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, classifyDialError(err, cfg)
	}
	// ssh.NewClientConn has no timeout of its own: a server that accepts the TCP
	// connection but never sends its banner (tarpit, overloaded sshd) would hang
	// it forever. ssh.Dial's Timeout only covers the TCP connect.
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		conn.Close()
		return nil, classifyDialError(err, cfg)
	}
	_ = conn.SetDeadline(time.Time{})

	pc := &pooledConn{
		client:      ssh.NewClient(sshConn, chans, reqs),
		fingerprint: captured,
		dead:        make(chan struct{}),
	}
	go func() {
		_ = pc.client.Wait()
		pc.kill()
	}()
	go pc.keepalive()
	return pc, nil
}

// acquire returns a live connection for cfg - the pooled one if it's healthy,
// otherwise a freshly dialed one. reused reports which, so the caller knows a
// failure to open a session may just mean the pooled connection went stale.
func acquire(cfg SSHConfig) (pc *pooledConn, reused bool, err error) {
	key := poolKey(cfg)

	pool.Lock()
	existing := pool.m[key]
	pool.Unlock()
	if existing != nil && !existing.isDead() {
		if cfg.HostFingerprint != "" && cfg.HostFingerprint != existing.fingerprint {
			return nil, false, &mitmError{expected: cfg.HostFingerprint, got: existing.fingerprint}
		}
		if existing.checkout() {
			return existing, true, nil
		}
	}

	fresh, err := dialConn(cfg)
	if err != nil {
		return nil, false, err
	}
	fresh.key = key

	pool.Lock()
	if other := pool.m[key]; other != nil && !other.isDead() && other.checkout() {
		// Lost a race with a concurrent dial for the same login: use the
		// established connection, drop ours.
		pool.Unlock()
		fresh.kill()
		return other, true, nil
	}
	pool.m[key] = fresh
	pool.Unlock()
	fresh.checkout()
	return fresh, false, nil
}

func (pc *pooledConn) isDead() bool {
	select {
	case <-pc.dead:
		return true
	default:
		return false
	}
}

// checkout marks the connection in use (blocking idle expiry); false means it
// is already being closed and the caller should dial a new one.
func (pc *pooledConn) checkout() bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.closing || pc.isDead() {
		return false
	}
	pc.active++
	if pc.idle != nil {
		pc.idle.Stop()
		pc.idle = nil
	}
	return true
}

func (pc *pooledConn) release() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.active--
	if pc.active == 0 && !pc.closing && !pc.isDead() {
		pc.idle = time.AfterFunc(poolIdleTimeout, pc.expire)
	}
}

// expire closes an idle connection; a command that started meanwhile keeps it.
func (pc *pooledConn) expire() {
	pc.mu.Lock()
	if pc.active > 0 || pc.closing {
		pc.mu.Unlock()
		return
	}
	pc.closing = true
	pc.mu.Unlock()
	pc.kill()
}

// kill closes the connection and forgets it. Safe to call repeatedly.
func (pc *pooledConn) kill() {
	pc.once.Do(func() {
		pc.mu.Lock()
		pc.closing = true
		pc.mu.Unlock()
		close(pc.dead)
		_ = pc.client.Close()
		pool.Lock()
		if pool.m[pc.key] == pc {
			delete(pool.m, pc.key)
		}
		pool.Unlock()
	})
}

// keepalive pings the server for the life of the connection. It keeps NAT and
// firewall state warm during quiet stretches of a long command, and - the part
// that matters more - notices a peer that silently stopped answering, which TCP
// alone can take many minutes to report.
func (pc *pooledConn) keepalive() {
	t := time.NewTicker(sshKeepAlive)
	defer t.Stop()
	for {
		select {
		case <-pc.dead:
			return
		case <-t.C:
			if !pc.ping() {
				pc.kill()
				return
			}
		}
	}
}

// ping reports whether the server answered. Any reply counts - OpenSSH answers
// an unknown request with "failure", which still proves the path is alive.
func (pc *pooledConn) ping() bool {
	res := make(chan error, 1)
	go func() {
		_, _, err := pc.client.SendRequest("keepalive@openssh.com", true, nil)
		res <- err
	}()
	select {
	case err := <-res:
		return err == nil
	case <-time.After(keepaliveReplyTimeout):
		return false
	case <-pc.dead:
		return true
	}
}

// CloseConnections drops every pooled connection (e.g. when the app exits).
func CloseConnections() {
	pool.Lock()
	conns := make([]*pooledConn, 0, len(pool.m))
	for _, pc := range pool.m {
		conns = append(conns, pc)
	}
	pool.Unlock()
	for _, pc := range conns {
		pc.kill()
	}
}
