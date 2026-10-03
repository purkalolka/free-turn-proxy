package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samosvalishe/free-turn-proxy/desktop/backend"
	"github.com/samosvalishe/free-turn-proxy/desktop/backend/serversetup"
	"github.com/samosvalishe/free-turn-proxy/desktop/backend/wintunnel"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/rtpopus"
	"github.com/samosvalishe/free-turn-proxy/mobile"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// eventSetupProgress carries the server-side script's live progress lines to the
// deploy wizard (one string per event).
const eventSetupProgress = "setup:progress"

var errNoSSHConfig = errors.New("для этого сервера нет сохранённых SSH-данных (он не был развёрнут этим приложением)")

// App is the Wails-bound facade the frontend calls into. It stays thin - all
// real logic lives in backend, so it can be unit-tested without a Wails runtime.
type App struct {
	ctx    context.Context
	store  *backend.Store
	engine *backend.Engine
	tray   interface{ Close() }
}

func NewApp() *App {
	store, err := backend.NewStore()
	if err != nil {
		// The store only fails on an unwritable config dir - nothing the user can
		// fix from inside the app, so fail fast rather than run half-alive.
		panic(fmt.Errorf("open server store: %w", err))
	}
	return &App{store: store, engine: backend.NewEngine(store)}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.engine.SetContext(ctx)
}

func (a *App) shutdown(ctx context.Context) {
	if a.tray != nil {
		a.tray.Close()
		a.tray = nil
	}
	a.engine.Disconnect()
}

// setupProgress forwards one progress line from a running server-side job to
// the frontend. Safe before startup (no context yet): the line is just dropped.
func (a *App) setupProgress(line string) {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, eventSetupProgress, line)
	}
}

func (a *App) SetTray(t interface{ Close() }) {
	a.tray = t
}

func (a *App) ShowWindow() {
	if a.ctx != nil {
		wailsruntime.WindowShow(a.ctx)
		wailsruntime.WindowUnminimise(a.ctx)
	}
}

func (a *App) Quit() {
	if a.ctx != nil {
		wailsruntime.Quit(a.ctx)
	}
}

func (a *App) ListServers() []backend.Server {
	return a.store.List()
}

func (a *App) NewServer() backend.Server {
	return backend.DefaultServer()
}

func (a *App) SaveServer(srv backend.Server) (backend.Server, error) {
	return a.store.Save(srv)
}

func (a *App) DeleteServer(id string) error {
	return a.store.Delete(id)
}

// ParseLink decodes a freeturn:// link for preview before the user imports it.
func (a *App) ParseLink(raw string) (*backend.ShareLink, error) {
	return backend.ParseShareLink(raw)
}

// ImportLink merges a freeturn:// link onto an existing (or blank, for a new
// profile) server and saves the result.
func (a *App) ImportLink(raw string, into backend.Server) (backend.Server, error) {
	link, err := backend.ParseShareLink(raw)
	if err != nil {
		return backend.Server{}, err
	}
	merged := link.ApplyTo(into)
	return a.store.Save(merged)
}

func (a *App) BuildLink(srv backend.Server) (string, error) {
	link := backend.ShareLink{
		Provider: srv.Provider, Peer: srv.Peer, Transport: srv.Transport, Mode: srv.Mode,
		ObfProfile: srv.ObfProfile, ObfKey: srv.ObfKey, N: srv.N, StreamsPerCred: srv.StreamsPerCred,
		ClientID: srv.ClientID, Listen: srv.Listen, DNSMode: srv.DNSMode, DNSServers: srv.DNSServers,
		ManualCaptcha: srv.ManualCaptcha, KCP: &srv.KCP, Name: srv.Name,
	}
	return link.Encode()
}

func (a *App) Connect(id string) error {
	srv, ok := a.store.Get(id)
	if !ok {
		return fmt.Errorf("server %q not found", id)
	}
	return a.engine.Connect(srv)
}

func (a *App) Disconnect() {
	a.engine.Disconnect()
}

func (a *App) Status() backend.Status {
	return a.engine.Status()
}

func (a *App) ConfigCommand(id string) (string, error) {
	srv, ok := a.store.Get(id)
	if !ok {
		return "", fmt.Errorf("server %q not found", id)
	}
	return a.engine.ConfigCommand(srv)
}

func (a *App) GenerateObfKey() (string, error) {
	return rtpopus.GenKeyHex()
}

func (a *App) GenerateClientID() string {
	return backend.GenerateClientID()
}

func (a *App) CoreVersion() string {
	return mobile.Version()
}

// VPNAvailable reports whether this build can run the embedded, one-click
// VPN mode (true on Windows) as opposed to relay-only.
func (a *App) VPNAvailable() bool {
	return wintunnel.Available()
}

// --- "Deploy new VPS" wizard: SSH into a bare server and provision it with
// the same server-control script the Android app uses (probe -> install ->
// wg-setup -> start), then save the result as a ready-to-use profile. ---

// SetupDetectRootMode runs a cheap preflight to work out how commands will
// need to escalate (root/sudo -n/sudo -S) before running the full script.
func (a *App) SetupDetectRootMode(cfg serversetup.SSHConfig) (serversetup.SSHConfig, error) {
	mode, fp, err := serversetup.DetectRootMode(cfg)
	if err != nil {
		return cfg, err
	}
	cfg.RootMode = mode
	if fp != "" {
		cfg.HostFingerprint = fp
	}
	return cfg, nil
}

// SetupProbe reads the target's current state (already installed? existing
// WireGuard port? conflicting software?) without changing anything.
func (a *App) SetupProbe(cfg serversetup.SSHConfig) (*serversetup.ProbeResult, error) {
	return serversetup.Probe(cfg)
}

func (a *App) SetupInstall(cfg serversetup.SSHConfig) (*serversetup.InstallResult, error) {
	return serversetup.Install(cfg, a.setupProgress)
}

// SetupWgSetup idempotently creates (or reuses) the managed ft-wg0 interface.
// endpoint should be the desktop client's own local listen address - it gets
// baked into the returned conf, which is meant for a separate WireGuard/
// AmneziaWG client pointed at this app's relay, not at the VPS directly.
func (a *App) SetupWgSetup(cfg serversetup.SSHConfig, port int, endpoint string) (*serversetup.WgSetupResult, error) {
	return serversetup.WgSetup(cfg, port, endpoint, a.setupProgress)
}

func (a *App) SetupStart(cfg serversetup.SSHConfig, opts serversetup.StartOptions) error {
	return serversetup.Start(cfg, opts, a.setupProgress)
}

// SetupCreateServer saves the freshly provisioned VPS as a server profile.
func (a *App) SetupCreateServer(cfg serversetup.SSHConfig, draft backend.ServerSetupDraft, wg *serversetup.WgSetupResult) (backend.Server, error) {
	srv := backend.BuildProvisionedServer(cfg, draft, wg)
	return a.store.Save(srv)
}

// --- Server management: control an already-deployed VPS over the same SSH
// credentials the "deploy new VPS" wizard saved on the profile. Unavailable
// for servers added by link or manual entry (they have no cfg.SSH). ---

func (a *App) serverSSH(id string) (backend.Server, serversetup.SSHConfig, error) {
	srv, ok := a.store.Get(id)
	if !ok {
		return backend.Server{}, serversetup.SSHConfig{}, fmt.Errorf("server %q not found", id)
	}
	if srv.SSH == nil {
		return backend.Server{}, serversetup.SSHConfig{}, errNoSSHConfig
	}
	return srv, *srv.SSH, nil
}

// ServerRemoteStatus re-probes the VPS (installed? running? existing conflicts?).
func (a *App) ServerRemoteStatus(id string) (*serversetup.ProbeResult, error) {
	_, ssh, err := a.serverSSH(id)
	if err != nil {
		return nil, err
	}
	return serversetup.Probe(ssh)
}

func (a *App) ServerStop(id string) error {
	_, ssh, err := a.serverSSH(id)
	if err != nil {
		return err
	}
	return serversetup.Stop(ssh)
}

// ServerRestart re-applies the profile's current listen/obfuscation/client-id
// settings on the VPS (cmd_start always stops any running process first, so
// this doubles as "push my local edits to the server").
func (a *App) ServerRestart(id string) error {
	srv, ssh, err := a.serverSSH(id)
	if err != nil {
		return err
	}
	listenPort := srv.ListenPort()
	if listenPort == "" {
		return fmt.Errorf("server %q has no listen port in its peer address", id)
	}
	return serversetup.Start(ssh, serversetup.StartOptions{
		Listen:      "0.0.0.0:" + listenPort,
		Connect:     fmt.Sprintf("127.0.0.1:%d", srv.WgPort),
		ObfProfile:  srv.ObfProfile,
		ObfKey:      srv.ObfKey,
		ObfTimingMs: srv.ObfTimingMs,
		ClientID:    srv.ClientID,
	}, a.setupProgress)
}

func (a *App) ServerLogs(id string, tail int) ([]string, error) {
	_, ssh, err := a.serverSSH(id)
	if err != nil {
		return nil, err
	}
	return serversetup.Logs(ssh, tail)
}

// ServerUninstall removes everything free-turn-proxy owns on the VPS. It does
// not delete the local profile - the user may still want the saved config
// (e.g. to redeploy later), so that stays a separate, explicit action.
func (a *App) ServerUninstall(id string, withWgPkg bool) (*serversetup.UninstallResult, error) {
	_, ssh, err := a.serverSSH(id)
	if err != nil {
		return nil, err
	}
	return serversetup.Uninstall(ssh, withWgPkg, false, a.setupProgress)
}

// --- WireGuard users: create, share (link / QR), delete, and see who connected
// when, over the same SSH credentials as the rest of server management. ---

// ServerPeers lists the server's WireGuard users with when each last connected.
func (a *App) ServerPeers(id string) (*serversetup.PeerList, error) {
	_, ssh, err := a.serverSSH(id)
	if err != nil {
		return nil, err
	}
	return serversetup.ShareList(ssh)
}

// ServerPeerAdd creates a user and returns their ready-to-send connection.
func (a *App) ServerPeerAdd(id, name string) (*backend.GuestShare, error) {
	srv, ssh, err := a.serverSSH(id)
	if err != nil {
		return nil, err
	}
	endpoint := srv.Listen
	if endpoint == "" {
		endpoint = "127.0.0.1:9000"
	}
	issued, err := serversetup.PeerAdd(ssh, name, endpoint, backend.GenerateClientID())
	if err != nil {
		return nil, err
	}
	share, err := a.guestShare(srv, ssh, issued, false)
	if err != nil {
		return nil, err
	}
	share.Name = strings.TrimSpace(name)
	return share, nil
}

// ServerPeerShare re-issues an existing user's link and QR. name is only used to
// label the allowlist entry of a user that predates it. includeVK puts the
// owner's own VK call link into the link (off unless the owner asks).
func (a *App) ServerPeerShare(id, pub, name string, includeVK bool) (*backend.GuestShare, error) {
	srv, ssh, err := a.serverSSH(id)
	if err != nil {
		return nil, err
	}
	issued, err := serversetup.PeerConf(ssh, pub, backend.GenerateClientID(), name)
	if err != nil {
		return nil, err
	}
	share, err := a.guestShare(srv, ssh, issued, includeVK)
	if err != nil {
		return nil, err
	}
	share.Name = strings.TrimSpace(name)
	return share, nil
}

// ServerPeerRemove deletes a user (their WireGuard peer and proxy access).
func (a *App) ServerPeerRemove(id, pub string) error {
	_, ssh, err := a.serverSSH(id)
	if err != nil {
		return err
	}
	return serversetup.PeerRemove(ssh, pub)
}

// guestShare turns an issued user config into the link + QR the UI shows. The
// obfuscation in the link comes from what the server is actually running, not
// only from the saved profile, since the user's client has to match it exactly.
func (a *App) guestShare(srv backend.Server, ssh serversetup.SSHConfig, issued *serversetup.PeerIssued, includeVK bool) (*backend.GuestShare, error) {
	in := backend.GuestLinkInput{ClientID: issued.ClientID, WgConf: issued.Conf, IncludeVK: includeVK}
	if info, err := serversetup.GetShareInfo(ssh); err == nil && info.Known {
		in.Mode, in.ObfProfile, in.ObfKey = info.Mode, info.ObfProfile, info.ObfKey
	}
	link, err := srv.GuestLink(in)
	if err != nil {
		return nil, err
	}
	qr, _ := backend.QRDataURL(link) // best effort: the link still works if it's too long for a QR
	return &backend.GuestShare{
		Pub: issued.PublicKey, IP: issued.IP, Link: link, QR: qr,
		IncludesVK: includeVK && len(srv.VKLinks) > 0,
	}, nil
}

// CopyText puts text on the system clipboard (the webview's own clipboard API is
// unreliable without a secure origin).
func (a *App) CopyText(text string) error {
	return wailsruntime.ClipboardSetText(a.ctx, text)
}
