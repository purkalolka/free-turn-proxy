package serversetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// When a VPS can't fetch the server binary from GitHub itself (throttled or
// blocked route, DNS trouble, no outbound HTTPS), the install used to just fail.
// The desktop machine almost always has a working connection to GitHub, so this
// path downloads the binary here, verifies it against the release's own
// checksums.txt, uploads it over the existing SSH connection and installs it
// with `install --local-bin` - no outbound access from the VPS needed at all.

const (
	releasesBase = "https://github.com/samosvalishe/free-turn-proxy/releases"
	apiLatestURL = "https://api.github.com/repos/samosvalishe/free-turn-proxy/releases/latest"
)

var (
	// A download that makes no progress for this long is considered dead.
	assetStallTimeout = 45 * time.Second
	assetAttempts     = 3
	uploadAttempts    = 3
)

// needsUploadFallback reports whether an install error code means "the server
// couldn't get the binary" (as opposed to a real problem uploading can't fix).
func needsUploadFallback(code string) bool {
	return code == "download_failed" || code == "version_resolve_failed"
}

// assetForMachine maps `uname -m` to the release asset name, mirroring
// detect_arch in 30-detect.sh for the architectures where the machine string
// alone decides. MIPS needs an endianness probe, so it is left to the
// server-side path.
func assetForMachine(m string) (string, bool) {
	m = strings.TrimSpace(m)
	switch {
	case m == "x86_64" || m == "amd64":
		return "server-linux-amd64", true
	case m == "aarch64" || m == "arm64":
		return "server-linux-arm64", true
	case m == "armv7l" || m == "armv6l" || m == "arm" || strings.HasPrefix(m, "armv5"):
		return "server-linux-armv7", true
	case m == "i386" || m == "i486" || m == "i586" || m == "i686":
		return "server-linux-386", true
	case m == "riscv64":
		return "server-linux-riscv64", true
	}
	return "", false
}

type serverBinary struct {
	data []byte
	tag  string
	sha  string // hex; empty when the release publishes no checksum for it
}

var tagInURL = regexp.MustCompile(`/releases/download/([^/]+)/`)

// resolveLatestTag finds the latest release tag from the redirect GitHub
// answers latest/download/<asset> with (not counted against the API's
// per-IP rate limit), falling back to the API.
func resolveLatestTag(ctx context.Context, asset string) (string, error) {
	noFollow := &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesBase+"/latest/download/"+asset, nil)
	if err == nil {
		if resp, derr := noFollow.Do(req); derr == nil {
			resp.Body.Close()
			if m := tagInURL.FindStringSubmatch(resp.Header.Get("Location")); m != nil {
				return m[1], nil
			}
		}
	}

	areq, err := http.NewRequestWithContext(ctx, http.MethodGet, apiLatestURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(areq)
	if err != nil {
		return "", fmt.Errorf("не удалось определить версию релиза: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Tag == "" {
		return "", errors.New("не удалось определить версию релиза")
	}
	return body.Tag, nil
}

// releaseChecksum reads the asset's sha256 from the release's checksums.txt.
// Best-effort: "" if the release publishes none.
func releaseChecksum(ctx context.Context, tag, asset string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesBase+"/download/"+tag+"/checksums.txt", nil)
	if err != nil {
		return ""
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == asset && len(f[0]) == 64 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// downloadStalling fetches url into memory, giving up if the transfer stalls
// (no bytes for `stall`) rather than after a fixed total time, so a slow but
// moving download is left alone.
func downloadStalling(ctx context.Context, url string, stall time.Duration, progress func(done, total int64)) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(stall, cancel)
	defer timer.Stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	stalled := func(err error) error {
		if errors.Is(err, context.Canceled) {
			return fmt.Errorf("загрузка зависла (нет данных %s)", stall)
		}
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, stalled(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	chunk := make([]byte, 64*1024)
	for {
		n, rerr := resp.Body.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			timer.Reset(stall)
			if progress != nil {
				progress(int64(buf.Len()), resp.ContentLength)
			}
		}
		if rerr == io.EOF {
			return buf.Bytes(), nil
		}
		if rerr != nil {
			return nil, stalled(rerr)
		}
	}
}

// fetchServerBinary downloads and verifies the latest server binary for asset.
func fetchServerBinary(ctx context.Context, asset string, say func(string)) (*serverBinary, error) {
	say("Определяю последнюю версию релиза…")
	tag, err := resolveLatestTag(ctx, asset)
	if err != nil {
		return nil, err
	}
	say(fmt.Sprintf("Скачиваю %s (%s) с GitHub…", asset, tag))
	want := releaseChecksum(ctx, tag, asset)

	var lastErr error
	for attempt := 1; attempt <= assetAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(retryDelay(attempt - 1))
		}
		lastMB := int64(-1)
		data, err := downloadStalling(ctx, releasesBase+"/download/"+tag+"/"+asset, assetStallTimeout, func(done, total int64) {
			if mb := done >> 20; mb != lastMB && mb > 0 {
				lastMB = mb
				if total > 0 {
					say(fmt.Sprintf("Скачано %d из %d МБ", mb, total>>20))
				} else {
					say(fmt.Sprintf("Скачано %d МБ", mb))
				}
			}
		})
		if err != nil {
			lastErr = fmt.Errorf("скачивание %s: %w", asset, err)
			continue
		}
		if len(data) < 100_000 || !bytes.HasPrefix(data, []byte("\x7fELF")) {
			lastErr = fmt.Errorf("скачанный %s не похож на исполняемый файл (%d байт)", asset, len(data))
			continue
		}
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if want != "" && got != want {
			lastErr = fmt.Errorf("контрольная сумма %s не совпала (ожидалась %s, получена %s)", asset, want, got)
			continue
		}
		if want == "" {
			say("У релиза нет контрольной суммы — проверяю только формат файла")
		}
		return &serverBinary{data: data, tag: tag, sha: want}, nil
	}
	return nil, lastErr
}

// uploadFile streams data to remotePath on the server (root-owned, mode 0600)
// and confirms the size that landed matches.
func uploadFile(cfg SSHConfig, remotePath string, data []byte) error {
	cmd := rootPrefix(cfg.RootMode) + "sh -c " + shellQuote(`umask 077; cat > "$1"`) + " ft-upload " + shellQuote(remotePath)

	var stdin io.Reader = bytes.NewReader(data)
	if cfg.RootMode == RootModeSudoPass {
		stdin = io.MultiReader(strings.NewReader(effectiveSudoPassword(cfg)+"\n"), stdin)
	}
	res, err := execReader(cfg, cmd, stdin)
	if err != nil {
		return err
	}
	if out := strings.TrimSpace(res.output); out != "" {
		return fmt.Errorf("сервер ответил при загрузке: %s", tailText(out, 200))
	}

	sizeCmd := rootPrefix(cfg.RootMode) + "sh -c " + shellQuote(`wc -c < "$1"`) + " ft-size " + shellQuote(remotePath)
	res, err = exec(cfg, sizeCmd, scriptStdin(cfg, ""))
	if err != nil {
		return err
	}
	n, perr := strconv.Atoi(strings.TrimSpace(res.output))
	if perr != nil || n != len(data) {
		return fmt.Errorf("файл на сервере неполный: %q байт из %d", strings.TrimSpace(res.output), len(data))
	}
	return nil
}

// installViaUpload is the fallback for an install whose server-side download
// failed: fetch the binary here, upload it, install it from the uploaded file.
func installViaUpload(cfg SSHConfig, onProgress func(string)) (*ControlResponse, string, error) {
	say := func(s string) {
		if onProgress != nil {
			onProgress(s)
		}
	}
	say("Сервер не смог скачать бинарник с GitHub — скачиваю на этом компьютере и загружаю на сервер по SSH")

	res, err := exec(cfg, "uname -m", "")
	if err != nil {
		return nil, res.fingerprint, err
	}
	machine := strings.TrimSpace(res.output)
	asset, ok := assetForMachine(machine)
	if !ok {
		return nil, res.fingerprint, fmt.Errorf("архитектура сервера %q: загрузка с компьютера не поддерживается", machine)
	}

	bin, err := fetchServerBinary(context.Background(), asset, say)
	if err != nil {
		return nil, res.fingerprint, err
	}

	remote := "/var/tmp/ft-upload-" + newJobID() + ".bin"
	say(fmt.Sprintf("Загружаю на сервер (%.1f МБ)…", float64(len(bin.data))/(1<<20)))
	var upErr error
	for attempt := 1; attempt <= uploadAttempts; attempt++ {
		if attempt > 1 {
			say(fmt.Sprintf("Повторная загрузка (попытка %d из %d): %v", attempt, uploadAttempts, upErr))
			time.Sleep(retryDelay(attempt - 1))
		}
		if upErr = uploadFile(cfg, remote, bin.data); upErr == nil {
			break
		}
	}
	if upErr != nil {
		return nil, res.fingerprint, fmt.Errorf("не удалось загрузить бинарник на сервер: %w", upErr)
	}

	argv := []string{"install", "--local-bin=" + remote, "--local-ver=" + bin.tag}
	if bin.sha != "" {
		argv = append(argv, "--sha256="+bin.sha)
	}
	resp, fp, err := runControlDetached(cfg, argv, onProgress)
	if err != nil || !resp.IsOK() {
		// Don't leave a 7 MB file behind in /var/tmp on failure (on success the
		// script consumed it).
		_, _ = exec(cfg, rootPrefix(cfg.RootMode)+"rm -f "+shellQuote(remote), scriptStdin(cfg, ""))
	}
	return resp, fp, err
}
