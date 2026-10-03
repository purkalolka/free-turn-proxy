package dtlsdial

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/dtls/v3"
)

type fakePC struct{}

func (fakePC) ReadFrom(_ []byte) (int, net.Addr, error) {
	select {}
}
func (fakePC) WriteTo(b []byte, _ net.Addr) (int, error) { return len(b), nil }
func (fakePC) Close() error                              { return nil }
func (fakePC) LocalAddr() net.Addr                       { return &net.UDPAddr{IP: net.IPv4zero} }
func (fakePC) SetDeadline(time.Time) error               { return nil }
func (fakePC) SetReadDeadline(time.Time) error           { return nil }
func (fakePC) SetWriteDeadline(time.Time) error          { return nil }

func TestDial_SemBlocksUntilCtx(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{}
	d := &Dialer{HandshakeSem: sem, HandshakeTimeout: 5 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	peer := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}
	_, err := d.Dial(ctx, fakePC{}, peer)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	raw := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"plain lowercase", raw, raw, false},
		{"uppercase", strings.ToUpper(raw), raw, false},
		{"with prefix sha256:", "sha256:" + raw, raw, false},
		{"with prefix SHA256:", "SHA256:" + raw, raw, false},
		{"with colons", "E3:B0:C4:42:98:FC:1C:14:9A:FB:F4:C8:99:6F:B9:24:27:AE:41:E4:64:9B:93:4C:A4:95:99:1B:78:52:B8:55", raw, false},
		{"with spaces", "  " + raw + "  ", raw, false},
		{"too short", "abc", "", true},
		{"too long", raw + "aa", "", true},
		{"invalid hex", strings.Repeat("z", 64), "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeFingerprint(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeFingerprint(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadOrGenerateCert(t *testing.T) {
	tmpDir := t.TempDir()
	certPath := filepath.Join(tmpDir, "server.crt")
	keyPath := filepath.Join(tmpDir, "server.key")

	// 1. First call generates and saves cert & key
	cert1, err := LoadOrGenerateCert(certPath, keyPath)
	if err != nil {
		t.Fatalf("first LoadOrGenerateCert failed: %v", err)
	}
	fp1 := CertificateFingerprint(cert1)
	if fp1 == "" {
		t.Fatal("empty fingerprint for generated cert")
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Logf("key file perm: %v", perm)
	}

	// 2. Second call loads the existing cert & key from disk
	cert2, err := LoadOrGenerateCert(certPath, keyPath)
	if err != nil {
		t.Fatalf("second LoadOrGenerateCert failed: %v", err)
	}
	fp2 := CertificateFingerprint(cert2)

	if fp1 != fp2 {
		t.Fatalf("fingerprint mismatch after reload: %s != %s", fp1, fp2)
	}
}

func TestDTLSFingerprintPinning(t *testing.T) {
	serverCert, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatalf("server cert gen: %v", err)
	}
	serverFP := CertificateFingerprint(serverCert)

	serverAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
	listener, err := dtls.ListenWithOptions("udp", serverAddr,
		dtls.WithCertificates(serverCert),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
	)
	if err != nil {
		t.Fatalf("dtls listener: %v", err)
	}
	defer listener.Close()

	resolvedAddr := listener.Addr().(*net.UDPAddr)

	go func() {
		for {
			conn, lerr := listener.Accept()
			if lerr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 100)
				n, _ := c.Read(buf)
				if n > 0 {
					_, _ = c.Write(buf[:n])
				}
			}(conn)
		}
	}()

	t.Run("ValidFingerprintSucceeds", func(t *testing.T) {
		clientPC, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("client listen: %v", err)
		}
		defer clientPC.Close()

		dialer := &Dialer{
			HandshakeTimeout:    3 * time.Second,
			ExpectedFingerprint: "sha256:" + serverFP,
		}

		conn, err := dialer.Dial(context.Background(), clientPC, resolvedAddr)
		if err != nil {
			t.Fatalf("dial with valid fingerprint failed: %v", err)
		}
		defer conn.Close()

		msg := []byte("ping")
		if _, err := conn.Write(msg); err != nil {
			t.Fatalf("write: %v", err)
		}
		buf := make([]byte, 10)
		n, err := conn.Read(buf)
		if err != nil || string(buf[:n]) != "ping" {
			t.Fatalf("read mismatch: %s, err: %v", string(buf[:n]), err)
		}
	})

	t.Run("MitmWrongFingerprintAborts", func(t *testing.T) {
		clientPC, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("client listen: %v", err)
		}
		defer clientPC.Close()

		fakeFPBytes := sha256.Sum256([]byte("fake-mitm-cert"))
		fakeFP := hex.EncodeToString(fakeFPBytes[:])

		dialer := &Dialer{
			HandshakeTimeout:    3 * time.Second,
			ExpectedFingerprint: fakeFP,
		}

		conn, err := dialer.Dial(context.Background(), clientPC, resolvedAddr)
		if err == nil {
			conn.Close()
			t.Fatal("expected handshake to fail due to fingerprint mismatch, but it succeeded")
		}
		if !strings.Contains(err.Error(), "fingerprint mismatch") {
			t.Fatalf("expected 'fingerprint mismatch' in error, got: %v", err)
		}
	})
}
