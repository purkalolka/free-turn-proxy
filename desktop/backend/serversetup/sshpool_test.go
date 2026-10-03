package serversetup

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecReusesOneConnection(t *testing.T) {
	shrinkTimings(t)
	poolIdleTimeout = time.Minute
	srv := newFakeSSHServer(t, func(cmd string, _ []byte) (string, int) { return "hi:" + cmd, 0 })
	cfg := srv.sshConfig()

	for i := 0; i < 3; i++ {
		res, err := exec(cfg, "echo", "")
		if err != nil || res.output != "hi:echo" {
			t.Fatalf("exec #%d = %q, %v", i, res.output, err)
		}
	}
	if n := srv.accepted.Load(); n != 1 {
		t.Fatalf("3 commands opened %d TCP connections, want 1 (pooled)", n)
	}
}

func TestExecRecoversFromDroppedPooledConnection(t *testing.T) {
	shrinkTimings(t)
	poolIdleTimeout = time.Minute
	srv := newFakeSSHServer(t, func(string, []byte) (string, int) { return "ok", 0 })
	cfg := srv.sshConfig()

	if _, err := exec(cfg, "one", ""); err != nil {
		t.Fatal(err)
	}
	srv.dropAll() // e.g. a NAT box forgot the idle connection
	res, err := exec(cfg, "two", "")
	if err != nil || res.output != "ok" {
		t.Fatalf("exec after drop = %q, %v; want transparent recovery", res.output, err)
	}
	if n := srv.accepted.Load(); n != 2 {
		t.Fatalf("accepted %d connections, want 2 (one redial)", n)
	}
}

func TestIdleConnectionExpires(t *testing.T) {
	shrinkTimings(t)
	srv := newFakeSSHServer(t, func(string, []byte) (string, int) { return "ok", 0 })
	cfg := srv.sshConfig()

	if _, err := exec(cfg, "one", ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * poolIdleTimeout)
	if _, err := exec(cfg, "two", ""); err != nil {
		t.Fatal(err)
	}
	if n := srv.accepted.Load(); n != 2 {
		t.Fatalf("accepted %d connections, want 2 (idle one closed)", n)
	}
}

func TestBusyConnectionIsNotExpired(t *testing.T) {
	shrinkTimings(t)
	// Command outlives poolIdleTimeout: the pool must not close it underneath.
	srv := newFakeSSHServer(t, func(string, []byte) (string, int) {
		time.Sleep(3 * poolIdleTimeout)
		return "done", 0
	})
	res, err := exec(srv.sshConfig(), "slow", "")
	if err != nil || res.output != "done" {
		t.Fatalf("long command = %q, %v", res.output, err)
	}
}

func TestHandshakeStallTimesOut(t *testing.T) {
	shrinkTimings(t)
	srv := newFakeSSHServer(t, func(string, []byte) (string, int) { return "ok", 0 })
	srv.silent.Store(true) // accepts TCP, never sends its banner

	start := time.Now()
	_, err := exec(srv.sshConfig(), "x", "")
	if err == nil {
		t.Fatal("expected an error from a server that never speaks")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %s to give up, handshake timeout is not applied", d)
	}
}

func TestBlackholedConnectionFailsInsteadOfHanging(t *testing.T) {
	shrinkTimings(t)
	release := make(chan struct{})
	srv := newFakeSSHServer(t, func(string, []byte) (string, int) {
		<-release // the command never finishes: the path is dead
		return "", 0
	})
	t.Cleanup(func() { close(release) })
	srv.mute.Store(true) // and keepalives go unanswered too

	errc := make(chan error, 1)
	go func() {
		_, err := exec(srv.sshConfig(), "hang", "")
		errc <- err
	}()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("expected an error once keepalives stopped being answered")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exec hung on a black-holed connection; keepalive failure must close it")
	}
}

func TestWrongPasswordFailsFastWithoutRetries(t *testing.T) {
	shrinkTimings(t)
	srv := newFakeSSHServer(t, func(string, []byte) (string, int) { return "ok", 0 })
	cfg := srv.sshConfig()
	cfg.Password = "nope"

	_, _, err := DetectRootMode(cfg)
	if err == nil || !strings.Contains(err.Error(), "не принял пароль") {
		t.Fatalf("err = %v, want a wrong-password message", err)
	}
	if n := srv.accepted.Load(); n != 1 {
		t.Fatalf("wrong password was attempted on %d connections, want exactly 1", n)
	}
}

func TestPinnedFingerprintMismatchIsRefused(t *testing.T) {
	shrinkTimings(t)
	poolIdleTimeout = time.Minute
	srv := newFakeSSHServer(t, func(string, []byte) (string, int) { return "ok", 0 })
	cfg := srv.sshConfig()

	first, err := exec(cfg, "x", "")
	if err != nil || first.fingerprint == "" {
		t.Fatalf("first exec: %v (fp %q)", err, first.fingerprint)
	}
	cfg.HostFingerprint = "SHA256:someOtherServersKey"
	_, err = exec(cfg, "x", "")
	var mitm *mitmError
	if !errors.As(err, &mitm) {
		t.Fatalf("err = %v, want a host-key mismatch even on a pooled connection", err)
	}
}
