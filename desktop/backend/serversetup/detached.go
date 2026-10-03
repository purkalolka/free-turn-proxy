package serversetup

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Long control commands (install downloads a binary and installs packages;
// wg-setup does the same) used to run as one blocking SSH session: if the link
// blipped anywhere in a several-minute window the remote script died with the
// session, the client saw nothing at all, and the whole thing started over.
//
// Here the command instead runs as a detached job on the server - it keeps going
// no matter what happens to our connection - while we poll it with short,
// independent SSH calls that each just read a status file. A dropped connection
// costs one poll; the next one reconnects and carries on. It also gives live
// progress: the script appends every log line to a file we read incrementally.
// (The same pattern Ansible calls "async + poll".)

var (
	pollInterval = 2 * time.Second
	// A job that hasn't finished by then is stuck, not slow.
	jobDeadline = 30 * time.Minute
	// How long the server may stay unreachable before we stop polling. The job
	// itself keeps running on the server regardless.
	unreachableLimit = 4 * time.Minute
)

// launchScript runs on the server (sh -c, root). $1 is the job id, the rest are
// the control script's own arguments; the script text itself arrives on stdin.
// Every positional is passed through as an argument, never interpolated into
// shell code, so no value can break the quoting.
//
// The job's runner and arguments go into FILES (run.sh, args) rather than onto
// the command line of whatever starts it: systemd-run parses its command line by
// systemd's own rules ("$$" collapses to "$", "%x" is a specifier), which silently
// corrupted the runner when it was passed inline. With files, the only thing on
// that command line is a plain path.
const launchScript = `J=/var/tmp/ftjob-$1; shift
if [ -f "$J/pid" ]; then echo FT_STARTED; exit 0; fi
umask 077
find /var/tmp -maxdepth 1 -name 'ftjob-*' -mmin +1440 -exec rm -rf {} + >/dev/null 2>&1
mkdir -p "$J" || exit 97
cat > "$J/control.sh" || exit 98
if [ $# -gt 0 ]; then printf '%s\n' "$@" > "$J/args"; else : > "$J/args"; fi
: > "$J/progress"
cat > "$J/run.sh" <<'FT_RUN'
echo $$ > "$FT_JOB/pid"
A=()
while IFS= read -r a || [ -n "$a" ]; do A+=("$a"); done < "$FT_JOB/args"
bash "$FT_JOB/control.sh" "${A[@]}" > "$FT_JOB/out" 2> "$FT_JOB/err" < /dev/null
echo $? > "$FT_JOB/rc"
FT_RUN
export FT_JOB="$J" FT_PROGRESS_FILE="$J/progress"
if [ -d /run/systemd/system ] && command -v systemd-run >/dev/null 2>&1 \
   && systemd-run -q --collect --unit "ftjob-${J##*-}" --setenv=FT_JOB="$J" --setenv=FT_PROGRESS_FILE="$J/progress" bash "$J/run.sh" >/dev/null 2>&1; then
    :
else
    SS=""; command -v setsid >/dev/null 2>&1 && SS=setsid
    $SS nohup bash "$J/run.sh" >/dev/null 2>&1 </dev/null &
fi
i=0
while [ ! -s "$J/pid" ] && [ "$i" -lt 100 ]; do sleep 0.1; i=$((i + 1)); done
if [ -s "$J/pid" ]; then echo FT_STARTED; else echo FT_NOSTART; exit 99; fi
`

// pollScript reports the job's state, the progress lines past offset $2, and -
// once it is no longer running - its stdout (the JSON envelope) and stderr tail.
const pollScript = `J=/var/tmp/ftjob-$1; OFF=${2:-0}
if [ ! -d "$J" ]; then echo "FT_STATE nojob"; exit 0; fi
if [ -f "$J/rc" ]; then ST="done $(cat "$J/rc" 2>/dev/null)"
elif [ -s "$J/pid" ] && kill -0 "$(cat "$J/pid")" 2>/dev/null; then ST=running
else ST=dead; fi
echo "FT_STATE $ST"
echo FT_PROGRESS
tail -n +$((OFF + 1)) "$J/progress" 2>/dev/null
echo FT_END
case "$ST" in
  running) ;;
  *) echo FT_OUT; cat "$J/out" 2>/dev/null; echo; echo FT_END
     echo FT_ERR; tail -c 2000 "$J/err" 2>/dev/null; echo; echo FT_END ;;
esac
`

const cleanupScript = `rm -rf "/var/tmp/ftjob-$1"`

func newJobID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// jobCommand wraps a fixed script in the root-mode prefix, passing args as
// positional parameters ($1...).
func jobCommand(cfg SSHConfig, script string, args ...string) string {
	return rootPrefix(cfg.RootMode) + "sh -c " + shellQuote(script) + " ft-job " + shellJoin(args)
}

// runControlDetached runs a control command as a detached server-side job (see
// above) and returns its envelope, like runControl. onProgress, if set, gets the
// script's log lines as they happen.
func runControlDetached(cfg SSHConfig, argv []string, onProgress func(string)) (*ControlResponse, string, error) {
	return runDetached(cfg, controlScript, argv, onProgress)
}

func runDetached(cfg SSHConfig, script string, argv []string, onProgress func(string)) (*ControlResponse, string, error) {
	if cfg.IP == "" {
		return nil, "", errors.New("no SSH config")
	}
	id := newJobID()
	fingerprint, err := launchJob(cfg, id, script, argv)
	if err != nil {
		return nil, fingerprint, err
	}
	return awaitJob(cfg, id, fingerprint, onProgress)
}

func launchJob(cfg SSHConfig, id, script string, argv []string) (string, error) {
	command := jobCommand(cfg, launchScript, append([]string{id}, argv...)...)
	stdin := scriptStdin(cfg, script)

	var fingerprint string
	var lastErr error
	for attempt := 0; attempt < controlAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(retryDelay(attempt))
		}
		res, err := exec(cfg, command, stdin)
		if res.fingerprint != "" {
			fingerprint = res.fingerprint
		}
		if err != nil {
			var exitErr *ssh.ExitError
			if errors.As(err, &exitErr) {
				switch exitErr.ExitStatus() {
				case 97:
					return fingerprint, errors.New("не удалось создать рабочий каталог задания в /var/tmp на сервере")
				case 98:
					return fingerprint, errors.New("не удалось записать скрипт задания на сервер (нет места на диске?)")
				case 99:
					return fingerprint, errors.New("фоновое задание не запустилось на сервере")
				}
			}
			if !isRetryable(err) {
				return fingerprint, err
			}
			lastErr = err
			continue
		}
		switch {
		case strings.Contains(res.output, "FT_STARTED"):
			return fingerprint, nil
		case strings.Contains(res.output, "FT_NOSTART"):
			return fingerprint, errors.New("фоновое задание не запустилось на сервере")
		default:
			// Not a transient failure (sudo refused, no shell...): the output says why.
			return fingerprint, fmt.Errorf("не удалось запустить задание на сервере: %s", tailText(res.output, 300))
		}
	}
	return fingerprint, fmt.Errorf("не удалось запустить задание на сервере после %d попыток: %w", controlAttempts, lastErr)
}

func awaitJob(cfg SSHConfig, id, fingerprint string, onProgress func(string)) (*ControlResponse, string, error) {
	deadline := time.Now().Add(jobDeadline)
	seen := 0
	var downSince time.Time
	var recent []string

	for {
		if time.Now().After(deadline) {
			return nil, fingerprint, fmt.Errorf("задание на сервере не завершилось за %s — оно могло зависнуть; последние строки: %s", jobDeadline, strings.Join(lastLines(recent, 3), "; "))
		}

		st, fp, err := pollJob(cfg, id, seen)
		if fp != "" {
			fingerprint = fp
		}
		if err != nil {
			if !isRetryable(err) {
				return nil, fingerprint, err
			}
			if downSince.IsZero() {
				downSince = time.Now()
			}
			if time.Since(downSince) > unreachableLimit {
				return nil, fingerprint, fmt.Errorf("потеряна связь с сервером: %w. Задание продолжает выполняться на сервере — повторите позже", err)
			}
			time.Sleep(pollInterval * 2)
			continue
		}
		downSince = time.Time{}

		seen += st.ProgressCount
		for _, l := range st.Progress {
			recent = append(recent, l)
			if onProgress != nil {
				onProgress(l)
			}
		}

		switch st.State {
		case "running":
			time.Sleep(pollInterval)
		case "done":
			cleanupJob(cfg, id)
			resp, perr := parseEnvelope(st.Out)
			if perr != nil {
				return nil, fingerprint, fmt.Errorf("скрипт на сервере завершился с кодом %d без корректного ответа: %v%s", st.ExitCode, perr, errSuffix(st.Err))
			}
			return resp, fingerprint, nil
		case "dead":
			cleanupJob(cfg, id)
			// Gone without writing an exit code: killed (out of memory, reboot...).
			// Whatever it did print may still be a complete envelope.
			if resp, perr := parseEnvelope(st.Out); perr == nil {
				return resp, fingerprint, nil
			}
			return nil, fingerprint, fmt.Errorf("процесс на сервере оборвался, не сообщив результат (возможно, нехватка памяти или перезагрузка); последние строки: %s%s", strings.Join(lastLines(recent, 3), "; "), errSuffix(st.Err))
		default: // nojob
			return nil, fingerprint, errors.New("задание пропало с сервера (он перезагружался или /var/tmp очищен) — повторите операцию")
		}
	}
}

func errSuffix(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	return "; stderr: " + tailText(stderr, 300)
}

func tailText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return strings.ReplaceAll(s, "\n", "; ")
}

// jobStatus is one poll's answer.
type jobStatus struct {
	State         string // running | done | dead | nojob
	ExitCode      int
	Progress      []string // non-empty lines new since the last poll
	ProgressCount int      // ALL lines new since the last poll (the offset advance)
	Out           string
	Err           string
}

func pollJob(cfg SSHConfig, id string, offset int) (*jobStatus, string, error) {
	command := jobCommand(cfg, pollScript, id, strconv.Itoa(offset))
	stdin := scriptStdin(cfg, "")
	res, err := exec(cfg, command, stdin)
	if err != nil {
		return nil, res.fingerprint, err
	}
	st, perr := parsePoll(res.output)
	if perr != nil {
		// Cut off mid-answer: treat like a dropped connection - poll again.
		return nil, res.fingerprint, perr
	}
	return st, res.fingerprint, nil
}

func cleanupJob(cfg SSHConfig, id string) {
	_, _ = exec(cfg, jobCommand(cfg, cleanupScript, id), scriptStdin(cfg, ""))
}

// parsePoll decodes pollScript's output. It insists on complete sections so a
// truncated answer is retried instead of being mistaken for a real state.
func parsePoll(output string) (*jobStatus, error) {
	st := &jobStatus{}
	haveState := false
	closed := map[string]bool{}
	section := ""
	var buf []string

	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case section == "" && strings.HasPrefix(line, "FT_STATE "):
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			st.State = f[1]
			if st.State == "done" && len(f) >= 3 {
				st.ExitCode, _ = strconv.Atoi(f[2])
			}
			haveState = true
		case section == "" && (line == "FT_PROGRESS" || line == "FT_OUT" || line == "FT_ERR"):
			section, buf = line, nil
		case section != "" && line == "FT_END":
			switch section {
			case "FT_PROGRESS":
				st.ProgressCount = len(buf)
				for _, l := range buf {
					if strings.TrimSpace(l) != "" {
						st.Progress = append(st.Progress, l)
					}
				}
			case "FT_OUT":
				st.Out = strings.TrimSpace(strings.Join(buf, "\n"))
			case "FT_ERR":
				st.Err = strings.TrimSpace(strings.Join(buf, "\n"))
			}
			closed[section] = true
			section = ""
		case section != "":
			buf = append(buf, line)
		}
	}

	if !haveState {
		return nil, fmt.Errorf("непонятный ответ на проверку задания: %.200s", strings.TrimSpace(output))
	}
	if st.State == "nojob" {
		return st, nil
	}
	if !closed["FT_PROGRESS"] || (st.State != "running" && (!closed["FT_OUT"] || !closed["FT_ERR"])) {
		return nil, errors.New("ответ на проверку задания оборван")
	}
	return st, nil
}
