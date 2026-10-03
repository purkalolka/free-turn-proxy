package serversetup

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// ProbeResult is the UI-friendly subset of cmd_probe's output the wizard needs.
type ProbeResult struct {
	HostFingerprint string `json:"hostFingerprint"`
	Installed       bool   `json:"installed"`
	Running         bool   `json:"running"`
	// WgPort is 0 when probe found no existing managed WireGuard interface.
	WgPort int `json:"wgPort"`
}

func Probe(cfg SSHConfig) (*ProbeResult, error) {
	resp, fp, err := runControl(cfg, []string{"probe"})
	if err != nil {
		return nil, err
	}
	if !resp.IsOK() {
		return nil, controlErr(resp)
	}
	var data probeData
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return nil, err
	}
	port := 0
	if data.Wg.Port != nil {
		port = *data.Wg.Port
	}
	return &ProbeResult{HostFingerprint: fp, Installed: data.Installed, Running: data.Running, WgPort: port}, nil
}

// InstallResult mirrors cmd_install's output.
type InstallResult struct {
	Version      string `json:"version"`
	Runtime      string `json:"runtime"`
	NeedsRestart bool   `json:"needsRestart"`
}

// Install downloads and installs the server binary. onProgress (optional)
// receives the server-side script's progress lines as they happen. If the
// server itself can't reach GitHub, the binary is downloaded on this machine
// and uploaded over SSH instead (see upload.go).
func Install(cfg SSHConfig, onProgress func(string)) (*InstallResult, error) {
	resp, _, err := runControlDetached(cfg, []string{"install"}, onProgress)
	if err != nil {
		return nil, err
	}
	if !resp.IsOK() && needsUploadFallback(resp.Code) {
		serverErr := controlErr(resp)
		resp, _, err = installViaUpload(cfg, onProgress)
		if err != nil {
			return nil, fmt.Errorf("%w\n(загрузка бинарника с этого компьютера тоже не удалась: %v)", serverErr, err)
		}
	}
	if !resp.IsOK() {
		return nil, controlErr(resp)
	}
	var data installData
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return nil, err
	}
	return &InstallResult{Version: data.Version, Runtime: data.Runtime, NeedsRestart: data.NeedsRestart}, nil
}

// WgSetupResult carries the (possibly pre-existing) WG port and the generated
// client conf, decoded from the base64 the script returns.
type WgSetupResult struct {
	Port       int    `json:"port"`
	ClientConf string `json:"clientConf"`
	Existed    bool   `json:"existed"`
}

// WgSetup idempotently bootstraps the managed ft-wg0 interface. endpoint is
// baked into the returned client conf's `Endpoint = ...` line - pass the
// desktop client's own local listen address (e.g. 127.0.0.1:9000) since this
// conf is meant to be pointed at the local relay, not at the VPS directly.
func WgSetup(cfg SSHConfig, port int, endpoint string, onProgress func(string)) (*WgSetupResult, error) {
	argv := []string{"wg-setup", fmt.Sprintf("--port=%d", port), "--endpoint=" + endpoint}
	resp, _, err := runControlDetached(cfg, argv, onProgress)
	if err != nil {
		return nil, err
	}
	if !resp.IsOK() {
		return nil, controlErr(resp)
	}
	var data wgSetupData
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return nil, err
	}
	conf := ""
	if data.ClientConfB64 != "" {
		if b, derr := base64.StdEncoding.DecodeString(data.ClientConfB64); derr == nil {
			conf = string(b)
		}
	}
	resultPort := port
	if data.Wg.Port > 0 {
		resultPort = data.Wg.Port
	}
	return &WgSetupResult{Port: resultPort, ClientConf: conf, Existed: data.Wg.Existed}, nil
}

type StartOptions struct {
	Listen      string `json:"listen"`
	Connect     string `json:"connect"`
	ObfProfile  string `json:"obfProfile"`
	ObfKey      string `json:"obfKey"`
	ObfTimingMs int    `json:"obfTimingMs"`
	ClientID    string `json:"clientId"`
}

// Start (re)starts the free-turn-proxy server with the given flags and, when
// ClientID is set, seeds it as the first allowed owner in clients.json.
func Start(cfg SSHConfig, opts StartOptions, onProgress func(string)) error {
	argv := []string{"start", "--listen=" + opts.Listen, "--connect=" + opts.Connect}
	if opts.ObfProfile != "" && opts.ObfProfile != "none" && opts.ObfKey != "" {
		argv = append(argv, "--obf-profile="+opts.ObfProfile, "--obf-key="+opts.ObfKey)
		if opts.ObfTimingMs > 0 {
			argv = append(argv, fmt.Sprintf("--obf-timing=%dms", opts.ObfTimingMs))
		}
	}
	if opts.ClientID != "" {
		argv = append(argv, "--client-id="+opts.ClientID)
	}
	resp, _, err := runControlDetached(cfg, argv, onProgress)
	if err != nil {
		return err
	}
	if !resp.IsOK() {
		return controlErr(resp)
	}
	return nil
}

func Stop(cfg SSHConfig) error {
	resp, _, err := runControl(cfg, []string{"stop"})
	if err != nil {
		return err
	}
	if !resp.IsOK() {
		return controlErr(resp)
	}
	return nil
}

// Logs tails the running server's journal (systemd) or log file (nohup fallback).
func Logs(cfg SSHConfig, tail int) ([]string, error) {
	if tail <= 0 {
		tail = 80
	}
	resp, _, err := runControl(cfg, []string{"logs", fmt.Sprintf("--tail=%d", tail)})
	if err != nil {
		return nil, err
	}
	if !resp.IsOK() {
		return nil, controlErr(resp)
	}
	return resp.Logs, nil
}

// UninstallResult mirrors cmd_uninstall's output.
type UninstallResult struct {
	DryRun       bool             `json:"dryRun"`
	Removed      UninstallRemoved `json:"removed"`
	WgPkgRemoved bool             `json:"wgPkgRemoved"`
	Kept         []string         `json:"kept"`
}

type UninstallRemoved struct {
	Binary     bool `json:"binary"`
	Unit       bool `json:"unit"`
	WgIface    bool `json:"wg_iface"`
	Prefix     bool `json:"prefix"`
	Sysctl     bool `json:"sysctl"`
	Ufw        bool `json:"ufw"`
	LegacyUnit bool `json:"legacy_unit"`
}

// Uninstall tears down everything free-turn-proxy owns on the VPS (binary,
// systemd unit, the managed ft-wg0 interface) - it never touches a foreign
// WireGuard setup or a live ip_forward sysctl (see Kept in the result).
func Uninstall(cfg SSHConfig, withWgPkg, dryRun bool, onProgress func(string)) (*UninstallResult, error) {
	argv := []string{"uninstall"}
	if withWgPkg {
		argv = append(argv, "--with-wg-pkg")
	}
	if dryRun {
		argv = append(argv, "--dry-run")
	}
	resp, _, err := runControlDetached(cfg, argv, onProgress)
	if err != nil {
		return nil, err
	}
	if !resp.IsOK() {
		return nil, controlErr(resp)
	}
	var data UninstallResult
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return nil, err
	}
	return &data, nil
}
