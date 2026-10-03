package serversetup

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

// execResult carries the host key fingerprint alongside stdout+stderr so
// callers can pin it (TOFU) for subsequent calls in the same wizard run, the
// same way the Android app threads SSHManager.lastSeenFingerprint through.
type execResult struct {
	output      string
	fingerprint string
}

// mitmError reports a host key that changed since a fingerprint was pinned -
// kept distinct from other errors so the UI can call out the risk explicitly.
type mitmError struct {
	expected, got string
}

func (e *mitmError) Error() string {
	return fmt.Sprintf("отпечаток SSH-сервера изменился (ожидался %s, получен %s) — возможна подмена сервера (MITM)", e.expected, e.got)
}

// exec runs command on cfg's host, optionally feeding text stdin, and combines
// stdout+stderr into one string like the Android client's `exec 2>&1` does. A
// non-zero remote exit status is not treated as a transport error - the control
// script always prints valid JSON on stdout before exiting non-zero (see
// 10-proto.sh's fail()/trap), so the caller decides success/failure from that
// JSON, not from the SSH exit code.
//
// Connections come from a pool (see sshpool.go) rather than being opened per
// call, and every one is watched by keepalives, so a path that dies mid-command
// surfaces as an error instead of an indefinite hang.
func exec(cfg SSHConfig, command, stdin string) (execResult, error) {
	var r io.Reader
	if stdin != "" {
		r = strings.NewReader(toLF(stdin))
	}
	return execReader(cfg, command, r)
}

// execReader is exec with an arbitrary (binary-safe) stdin. The one internal
// retry (a stale pooled connection) happens before anything reads stdin, so the
// same reader is safe to reuse.
func execReader(cfg SSHConfig, command string, stdin io.Reader) (execResult, error) {
	for attempt := 0; ; attempt++ {
		pc, reused, err := acquire(cfg)
		if err != nil {
			return execResult{}, err
		}
		session, err := pc.client.NewSession()
		if err != nil {
			// Opening a session on a pooled connection that died while idle
			// (NAT/firewall dropped it) fails before anything ran, so retrying
			// once on a fresh connection is always safe.
			pc.kill()
			pc.release()
			if reused && attempt == 0 {
				continue
			}
			return execResult{fingerprint: pc.fingerprint}, err
		}
		res, err := runSession(pc, session, command, stdin)
		pc.release()
		return res, err
	}
}

func runSession(pc *pooledConn, session *ssh.Session, command string, stdin io.Reader) (execResult, error) {
	defer session.Close()

	// Separate buffers, joined afterwards. The library drains stdout and stderr
	// on two goroutines at once; pointing both at ONE bytes.Buffer is a data race
	// in which the stderr copier (idle until the channel closes) can finish last
	// and reset the buffer's length, silently discarding everything stdout wrote.
	// The result was an empty response with no error - at random - which the
	// caller could only report as "the server returned nothing".
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if stdin != nil {
		session.Stdin = stdin
	}

	runErr := session.Run(command)
	out := combineOutput(stdout.String(), stderr.String())
	res := execResult{output: out, fingerprint: pc.fingerprint}

	var exitErr *ssh.ExitError
	var missing *ssh.ExitMissingError
	switch {
	case runErr == nil, errors.As(runErr, &exitErr):
		// Ran to completion (non-zero exit is the script's business, not ours).
		if out == "" && runErr != nil {
			return execResult{fingerprint: pc.fingerprint}, runErr
		}
		return res, nil
	case errors.As(runErr, &missing) && out != "":
		// Some minimal SSH servers never send an exit status; the output is fine.
		return res, nil
	default:
		// Transport failure mid-command: whatever arrived is unreliable, and the
		// connection is no good for the next command either.
		pc.kill()
		return execResult{fingerprint: pc.fingerprint}, runErr
	}
}

// combineOutput joins stdout and stderr the way `2>&1` would present them for a
// script whose only stdout is one JSON line: the JSON first, any stderr noise
// after it (the envelope parser finds the JSON line wherever it sits).
func combineOutput(stdout, stderr string) string {
	if stderr == "" {
		return stdout
	}
	if stdout != "" && !strings.HasSuffix(stdout, "\n") {
		stdout += "\n"
	}
	return stdout + stderr
}

func toLF(s string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
}

// classifyDialError turns golang.org/x/crypto/ssh's terse dial-time errors
// into something the user can act on. Auth failures in particular are
// ambiguous by construction: "attempted methods [none]" (no other method even
// listed) usually means the server's advertised methods didn't include the
// one we tried at all - e.g. PasswordAuthentication is off and only a key is
// accepted - not that the password itself was wrong, so we name both
// possibilities rather than guessing.
func classifyDialError(err error, cfg SSHConfig) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		if cfg.AuthType == AuthSSHKey {
			return fmt.Errorf("SSH: сервер не принял ключ (неверный ключ/passphrase, либо для %s разрешён только вход по паролю): %w", cfg.Username, err)
		}
		return fmt.Errorf("SSH: сервер не принял пароль (неверный пароль, либо для %s отключён вход по паролю и нужен SSH-ключ): %w", cfg.Username, err)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("SSH: сервер отказал в соединении на %s:%d — проверьте IP и порт: %w", cfg.IP, cfg.Port, err)
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out") || strings.Contains(msg, "deadline"):
		return fmt.Errorf("SSH: %s:%d не отвечает (таймаут) — проверьте IP/порт и firewall на сервере: %w", cfg.IP, cfg.Port, err)
	case strings.Contains(msg, "no route to host") || strings.Contains(msg, "network is unreachable"):
		return fmt.Errorf("SSH: %s недостижим — проверьте IP: %w", cfg.IP, err)
	default:
		return err
	}
}

func fingerprintOf(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func authMethodsFor(cfg SSHConfig) ([]ssh.AuthMethod, error) {
	if cfg.AuthType == AuthSSHKey {
		key := []byte(toLF(cfg.SSHKey))
		var signer ssh.Signer
		var err error
		if cfg.Password != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(cfg.Password))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, fmt.Errorf("разбор приватного ключа: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	if cfg.Password == "" {
		return nil, errors.New("password is required")
	}
	// Offer both: OpenSSH with PAM (the default on most Ubuntu/Debian cloud
	// images) advertises "keyboard-interactive" for password-style logins,
	// not the plain "password" method - a client offering only ssh.Password
	// fails auth outright even with a correct password, because the two
	// methods are negotiated separately and the server never listed
	// "password" as acceptable in the first place.
	password := cfg.Password
	return []ssh.AuthMethod{
		ssh.Password(password),
		ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = password
			}
			return answers, nil
		}),
	}, nil
}

// safeShellArg matches tokens that never need quoting (mirrors ServerControl.kt's shellQuote).
var safeShellArg = regexp.MustCompile(`^[A-Za-z0-9._:=/-]+$`)

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if safeShellArg.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}
