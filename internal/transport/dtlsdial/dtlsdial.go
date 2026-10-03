// Package dtlsdial настраивает DTLS-клиент с self-signed сертификатами,
// certificate pinning (SHA-256 fingerprint) и ограничением параллельных handshake.
package dtlsdial

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
)

// GenerateSelfSignedCert генерирует новый самоподписанный сертификат.
func GenerateSelfSignedCert() (tls.Certificate, error) {
	return selfsign.GenerateSelfSigned()
}

// CertificateFingerprint возвращает hex-строку sha256 отпечатка первого (leaf) сертификата в цепочке.
func CertificateFingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	return RawCertificateFingerprint(cert.Certificate[0])
}

// RawCertificateFingerprint возвращает hex-строку sha256 отпечатка DER-байтов сертификата.
func RawCertificateFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// NormalizeFingerprint очищает и проверяет формат sha256 отпечатка (убирает sha256:, двоеточия, пробелы).
func NormalizeFingerprint(fp string) (string, error) {
	s := strings.TrimSpace(fp)
	if strings.HasPrefix(strings.ToLower(s), "sha256:") {
		s = s[7:]
	}
	s = strings.ReplaceAll(s, ":", "")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ToLower(s)
	if len(s) != 64 {
		return "", fmt.Errorf("dtlsdial: invalid fingerprint length %d (expected 64 hex characters)", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", fmt.Errorf("dtlsdial: invalid hex characters in fingerprint: %w", err)
	}
	return s, nil
}

// LoadOrGenerateCert загружает существующий X.509 сертификат и ключ из файлов
// либо генерирует новый и сохраняет его на диск с правами 0600.
func LoadOrGenerateCert(certPath, keyPath string) (tls.Certificate, error) {
	if certPath != "" && keyPath != "" {
		if _, err := os.Stat(certPath); err == nil {
			if _, kerr := os.Stat(keyPath); kerr == nil {
				return tls.LoadX509KeyPair(certPath, keyPath)
			}
		}
	}

	cert, err := GenerateSelfSignedCert()
	if err != nil {
		return tls.Certificate{}, err
	}

	if certPath != "" && keyPath != "" {
		if err := saveCertAndKey(cert, certPath, keyPath); err != nil {
			return cert, fmt.Errorf("dtlsdial: save cert and key: %w", err)
		}
	}
	return cert, nil
}

func saveCertAndKey(cert tls.Certificate, certPath, keyPath string) error {
	if len(cert.Certificate) == 0 {
		return errors.New("dtlsdial: certificate chain is empty")
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Certificate[0],
	})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyDER,
	})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	return nil
}

// Dialer конфигурирует DTLS-handshake клиента.
type Dialer struct {
	HandshakeTimeout    time.Duration
	HandshakeSem        chan struct{}
	ExpectedFingerprint string // SHA-256 отпечаток сертификата сервера ("sha256:..." или hex)
}

// Dial выполняет DTLS-handshake поверх pc к peer с уникальным self-signed сертификатом.
// Если ExpectedFingerprint задан, сертификат сервера проверяется на точное совпадение отпечатка.
func (d *Dialer) Dial(ctx context.Context, pc net.PacketConn, peer *net.UDPAddr) (*dtls.Conn, error) {
	certificate, err := GenerateSelfSignedCert()
	if err != nil {
		return nil, err
	}
	if d.HandshakeSem != nil {
		select {
		case d.HandshakeSem <- struct{}{}:
			defer func() { <-d.HandshakeSem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	hsCtx := ctx
	if d.HandshakeTimeout > 0 {
		var cancel context.CancelFunc
		hsCtx, cancel = context.WithTimeout(ctx, d.HandshakeTimeout)
		defer cancel()
	}

	opts := []dtls.ClientOption{
		dtls.WithCertificates(certificate),
		dtls.WithInsecureSkipVerify(true),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
		dtls.WithConnectionIDGenerator(dtls.OnlySendCIDGenerator()),
	}

	if d.ExpectedFingerprint != "" {
		expectedFP, err := NormalizeFingerprint(d.ExpectedFingerprint)
		if err != nil {
			return nil, fmt.Errorf("dtlsdial: invalid expected fingerprint: %w", err)
		}

		verifier := func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("dtlsdial: peer did not present any certificates")
			}
			gotFP := RawCertificateFingerprint(rawCerts[0])
			if !strings.EqualFold(gotFP, expectedFP) {
				return fmt.Errorf("dtlsdial: peer certificate fingerprint mismatch (got sha256:%s, expected sha256:%s)", gotFP, expectedFP)
			}
			return nil
		}
		opts = append(opts, dtls.WithVerifyPeerCertificate(verifier))
	}

	dtlsConn, err := dtls.ClientWithOptions(pc, peer, opts...)
	if err != nil {
		return nil, err
	}
	if err := dtlsConn.HandshakeContext(hsCtx); err != nil {
		_ = dtlsConn.Close()
		return nil, err
	}
	return dtlsConn, nil
}
