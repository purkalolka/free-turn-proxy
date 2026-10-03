package serversetup

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const controlAttempts = 5

var errEmptyResponse = errors.New("empty response")

// retryUnit scales the pause between retries (a var so tests can shrink it).
var retryUnit = 2 * time.Second

// retryDelay is the pause before retry number attempt (1-based): 2s, 4s, 6s...
func retryDelay(attempt int) time.Duration {
	return time.Duration(attempt) * retryUnit
}

// scriptStdin is what goes on stdin for a command run through sudo -S: the
// password as its first line (sudo consumes it), then the payload.
func scriptStdin(cfg SSHConfig, payload string) string {
	if cfg.RootMode == RootModeSudoPass {
		return effectiveSudoPassword(cfg) + "\n" + payload
	}
	return payload
}

// runControl streams the control script to cfg's host via stdin and runs
// argv against it (mirrors ServerControl.kt's `run`), returning the parsed
// envelope and the fingerprint seen on this connection.
//
// It is for quick commands (probe, logs, stop...). Anything that can run for
// minutes - package installs, downloads - goes through runControlDetached
// instead, because holding one SSH session open that long is exactly what a
// flaky link kills.
//
// The script always prints exactly one JSON line before exiting (10-proto.sh's
// ok()/fail()/EXIT trap), so an empty or unparseable response means the SSH
// session ended (dropped connection, server-side hiccup) before the script
// finished. Transport errors are similarly often transient. Both get a few
// retries on a fresh connection - except auth/host-key failures, which
// retrying can't fix and which for a MITM mismatch specifically must not be
// silently retried past.
func runControl(cfg SSHConfig, argv []string) (*ControlResponse, string, error) {
	if cfg.IP == "" {
		return nil, "", errors.New("no SSH config")
	}

	stdin := scriptStdin(cfg, controlScript)
	command := remoteCommand(argv, cfg.RootMode)

	var fingerprint string
	var lastErr error
	var lastOut string
	for attempt := 0; attempt < controlAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(retryDelay(attempt))
		}
		res, err := exec(cfg, command, stdin)
		if res.fingerprint != "" {
			fingerprint = res.fingerprint
		}
		if err != nil {
			if !isRetryable(err) {
				return nil, fingerprint, err
			}
			lastErr, lastOut = err, ""
			continue
		}
		resp, perr := parseEnvelope(res.output)
		if perr != nil {
			// Truncated or garbled: the connection most likely died mid-output.
			lastErr, lastOut = perr, res.output
			continue
		}
		return resp, fingerprint, nil
	}
	return nil, fingerprint, finalError(argv, lastErr, lastOut)
}

func finalError(argv []string, err error, output string) error {
	cmd := strings.Join(argv, " ")
	switch {
	case errors.Is(err, errEmptyResponse):
		return fmt.Errorf(
			"сервер не вернул ответа на команду %q после %d попыток (соединение обрывается до того, как скрипт успевает что-то напечатать) — проверьте стабильность сети до сервера и попробуйте ещё раз",
			cmd, controlAttempts,
		)
	case output != "":
		return fmt.Errorf("неожиданный ответ сервера на команду %q после %d попыток: %w", cmd, controlAttempts, err)
	default:
		return fmt.Errorf("команда %q не выполнилась после %d попыток: %w", cmd, controlAttempts, err)
	}
}

// parseEnvelope extracts the protocol envelope from a command's combined
// stdout+stderr. It is the last line that parses as one, so noise printed before
// it (sudo's "unable to resolve host", a login banner, a stray warning) doesn't
// break an otherwise complete response.
func parseEnvelope(output string) (*ControlResponse, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return nil, errEmptyResponse
	}
	lines := strings.Split(trimmed, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var resp ControlResponse
		if err := json.Unmarshal([]byte(line), &resp); err == nil && resp.Proto != 0 {
			return &resp, nil
		}
	}
	return nil, fmt.Errorf("неожиданный ответ сервера (%.300s)", trimmed)
}

// isRetryable is false for failures a retry can't fix: bad credentials, a
// changed host key (never silently retry past a possible MITM), or a bad
// local config. Everything else (dial/timeout/reset/EOF) is assumed transient.
func isRetryable(err error) bool {
	var mitm *mitmError
	if errors.As(err, &mitm) {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"unable to authenticate", "password is required", "разбор приватного ключа", "подмена сервера"} {
		if strings.Contains(msg, s) {
			return false
		}
	}
	return true
}

// isDefinitive marks dial failures where waiting won't change the answer (wrong
// IP/port, closed port) - used for the wizard's first contact, where a typo
// should fail in seconds rather than after a full retry schedule.
func isDefinitive(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "отказал в соединении") || strings.Contains(msg, "недостижим")
}

// effectiveSudoPassword: SUDO_PASS sends the password as stdin's first line
// (sudo -S consumes it, the rest reaches bash). Blank for key-auth without an
// explicit sudo password - sudo then fails with a clear sudo_auth_failed
// rather than leaking a login password that was never meant for sudo.
func effectiveSudoPassword(cfg SSHConfig) string {
	if cfg.SudoPassword != "" {
		return cfg.SudoPassword
	}
	if cfg.AuthType == AuthPassword {
		return cfg.Password
	}
	return ""
}

// rootPrefix is the escalation prefix for a command in the given root mode.
func rootPrefix(rootMode string) string {
	switch rootMode {
	case RootModeSudoNoPass:
		return "sudo -n "
	case RootModeSudoPass:
		// -k drops the cached sudo timestamp first - otherwise a still-warm
		// timestamp can skip the password prompt and it leaks into stderr as
		// an unrecognized command instead.
		return "sudo -k -S -p '' "
	default:
		return ""
	}
}

func remoteCommand(argv []string, rootMode string) string {
	return rootPrefix(rootMode) + "bash -s -- " + shellJoin(argv)
}

// controlErr turns a result:"err" envelope into a Go error. The script's own
// log lines ride along: they usually hold the actual reason (the package
// manager's error text, the failed download's curl message) that the terse
// code/msg pair only hints at.
func controlErr(resp *ControlResponse) error {
	msg := resp.Msg
	if msg == "" {
		msg = resp.Code
	}
	text := fmt.Sprintf("%s: %s", resp.Code, msg)
	if resp.Stage != "" {
		text = fmt.Sprintf("[%s] %s", resp.Stage, text)
	}
	if tail := lastLines(resp.Logs, 4); len(tail) > 0 {
		text += "\n" + strings.Join(tail, "\n")
	}
	return errors.New(text)
}

func lastLines(lines []string, n int) []string {
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// DetectRootMode runs the same cheap preflight the Android app uses, without
// streaming the (much larger) control script.
func DetectRootMode(cfg SSHConfig) (rootMode string, fingerprint string, err error) {
	const probe = `id -u; command -v sudo >/dev/null 2>&1 && { sudo -n true 2>/dev/null && echo FT_SUDO_NOPASS || echo FT_SUDO_PASS; }`
	const attempts = 3
	var res execResult
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			time.Sleep(retryDelay(attempt))
		}
		res, err = exec(cfg, probe, "")
		if err == nil {
			break
		}
		if !isRetryable(err) || isDefinitive(err) {
			return "", res.fingerprint, err
		}
	}
	if err != nil {
		return "", res.fingerprint, err
	}
	return classifyRootMode(res.output), res.fingerprint, nil
}

func classifyRootMode(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		n, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		if n == 0 {
			return RootModeRoot
		}
		break
	}
	switch {
	case strings.Contains(output, "FT_SUDO_NOPASS"):
		return RootModeSudoNoPass
	case strings.Contains(output, "FT_SUDO_PASS"):
		return RootModeSudoPass
	default:
		return RootModeRoot
	}
}
