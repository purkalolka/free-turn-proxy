package serversetup

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeSSHServer is a minimal in-process SSH server: password auth ("pw"), exec
// requests answered by a handler, and switches to simulate the failures that
// matter here (connections dropped, a peer that stops answering, a server that
// accepts TCP but never speaks).
type fakeSSHServer struct {
	ln      net.Listener
	cfg     *ssh.ServerConfig
	handler func(cmd string, stdin []byte) (out string, exit int)
	done    chan struct{}

	mu    sync.Mutex
	conns []net.Conn

	accepted atomic.Int32 // TCP connections accepted
	silent   atomic.Bool  // accept TCP, never send the SSH banner
	mute     atomic.Bool  // stop answering global requests (keepalives)
}

func newFakeSSHServer(t *testing.T, handler func(cmd string, stdin []byte) (string, int)) *fakeSSHServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == "pw" {
				return nil, nil
			}
			return nil, errors.New("bad password")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSSHServer{ln: ln, cfg: cfg, handler: handler, done: make(chan struct{})}
	go s.acceptLoop()
	t.Cleanup(s.close)
	return s
}

// sshConfig is a client config that reaches this server.
func (s *fakeSSHServer) sshConfig() SSHConfig {
	host, portStr, _ := net.SplitHostPort(s.ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return SSHConfig{IP: host, Port: port, Username: "root", Password: "pw", AuthType: AuthPassword, RootMode: RootModeRoot}
}

func (s *fakeSSHServer) close() {
	select {
	case <-s.done:
		return
	default:
		close(s.done)
	}
	s.ln.Close()
	s.dropAll()
}

// dropAll abruptly closes every open connection.
func (s *fakeSSHServer) dropAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
	s.conns = nil
}

func (s *fakeSSHServer) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.accepted.Add(1)
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		go s.serve(conn)
	}
}

func (s *fakeSSHServer) serve(conn net.Conn) {
	if s.silent.Load() {
		<-s.done
		return
	}
	sc, chans, reqs, err := ssh.NewServerConn(conn, s.cfg)
	if err != nil {
		conn.Close()
		return
	}
	defer sc.Close()
	go func() {
		for r := range reqs {
			if s.mute.Load() {
				continue // a black-holed peer: read, never answer
			}
			if r.WantReply {
				_ = r.Reply(false, nil)
			}
		}
	}()
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.session(ch, chReqs)
	}
}

func (s *fakeSSHServer) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		var p struct{ Cmd string }
		_ = ssh.Unmarshal(req.Payload, &p)
		_ = req.Reply(true, nil)
		stdin, _ := io.ReadAll(ch)
		out, code := s.handler(p.Cmd, stdin)
		_, _ = io.WriteString(ch, out)
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
		return
	}
}

// shrinkTimings makes every SSH timeout tiny for the duration of a test.
func shrinkTimings(t *testing.T) {
	t.Helper()
	oldDial, oldHS, oldKA, oldReply, oldIdle, oldRetry := dialTimeout, handshakeTimeout, sshKeepAlive, keepaliveReplyTimeout, poolIdleTimeout, retryUnit
	dialTimeout, handshakeTimeout = 2*time.Second, 400*time.Millisecond
	sshKeepAlive, keepaliveReplyTimeout = 100*time.Millisecond, 300*time.Millisecond
	poolIdleTimeout, retryUnit = 300*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() {
		CloseConnections()
		dialTimeout, handshakeTimeout, sshKeepAlive, keepaliveReplyTimeout, poolIdleTimeout, retryUnit = oldDial, oldHS, oldKA, oldReply, oldIdle, oldRetry
	})
}
