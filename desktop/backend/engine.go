package backend

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/samosvalishe/free-turn-proxy/desktop/backend/notify"
	"github.com/samosvalishe/free-turn-proxy/desktop/backend/wintunnel"
	"github.com/samosvalishe/free-turn-proxy/mobile"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Status is a UI-friendly snapshot of the running (or last) session.
type Status struct {
	State    string `json:"state"` // idle | connecting | connected | captcha | error
	Streams  int    `json:"streams"`
	Total    int    `json:"total"`
	ErrMsg   string `json:"errMsg"`
	TxRate   int64  `json:"txRate"`
	RxRate   int64  `json:"rxRate"`
	TxTotal  int64  `json:"txTotal"`
	RxTotal  int64  `json:"rxTotal"`
	ServerID string `json:"serverId"`
}

// LogLine is pushed to the frontend over the "core:log" event as the session runs.
type LogLine struct {
	Level string `json:"level"`
	Msg   string `json:"msg"`
	AtMs  int64  `json:"atMs"`
}

const (
	eventLog     = "core:log"
	eventState   = "core:state"
	eventCaptcha = "core:captcha"
)

// Engine drives the in-process free-turn-proxy core (github.com/.../mobile) and
// bridges its callbacks to Wails frontend events. Only one session can run at a
// time - that mirrors the core's own single "current session" model.
type Engine struct {
	ctx   context.Context
	store *Store

	mu       sync.Mutex
	serverID string
}

func NewEngine(store *Store) *Engine {
	e := &Engine{store: store}
	mobile.SetStateDir(store.StateDir())
	mobile.SetEventSink(e)
	return e
}

func (e *Engine) SetContext(ctx context.Context) { e.ctx = ctx }

func (e *Engine) Connect(srv Server) error {
	if srv.Peer == "" {
		return errors.New("peer is required")
	}
	vpn := srv.ConnMode == ConnModeVPN
	if vpn && srv.WgClientConf == "" {
		return errors.New("VPN mode needs a WireGuard config - none saved on this server")
	}
	cfg, err := srv.ToCoreConfig().JSON()
	if err != nil {
		return err
	}
	if verr := mobile.ValidateConfig(cfg); verr != "" {
		return errors.New(verr)
	}

	// Unconditionally clear whatever might already be running, including a
	// half-failed previous attempt (e.g. the adapter came up but the tunnel
	// handshake didn't) that would otherwise block every further Connect with
	// "already running"/"already connected" and leave no way back except
	// force-stop. Stop/Disconnect are safe no-ops when nothing is up.
	e.forceStop()

	e.mu.Lock()
	e.serverID = srv.ID
	e.mu.Unlock()

	if vpn {
		var exclusions []string
		if srv.SSH != nil && srv.SSH.IP != "" {
			exclusions = append(exclusions, srv.SSH.IP)
		}
		if srv.Peer != "" {
			if host, _, err := net.SplitHostPort(srv.Peer); err == nil && net.ParseIP(host) != nil {
				if srv.SSH == nil || host != srv.SSH.IP {
					exclusions = append(exclusions, host)
				}
			}
		}
		return wintunnel.Connect(cfg, srv.WgClientConf, DefaultTunnelMTU, exclusions...)
	}
	return mobile.Start(cfg)
}

func (e *Engine) Disconnect() {
	e.forceStop()
}

func (e *Engine) forceStop() {
	mobile.Stop()
	wintunnel.Disconnect()
}

func (e *Engine) Status() Status {
	snap := mobile.GetState()
	e.mu.Lock()
	serverID := e.serverID
	e.mu.Unlock()
	return Status{
		State: snap.State, Streams: snap.Streams, Total: snap.Total, ErrMsg: snap.ErrMsg,
		TxRate: snap.TxRate, RxRate: snap.RxRate, TxTotal: snap.TxTotal, RxTotal: snap.RxTotal,
		ServerID: serverID,
	}
}

// ConfigCommand returns the equivalent `client ...` CLI invocation for a profile,
// for users who want to sanity-check or copy it (mirrors the Android "command" screen).
func (e *Engine) ConfigCommand(srv Server) (string, error) {
	cfg, err := srv.ToCoreConfig().JSON()
	if err != nil {
		return "", err
	}
	args, err := mobile.ConfigToArgs(cfg)
	if err != nil {
		return "", err
	}
	return "client " + args, nil
}

// --- mobile.EventSink ---

func (e *Engine) OnState(state string, streams, total int, errMsg string) {
	if e.ctx == nil {
		return
	}
	e.mu.Lock()
	serverID := e.serverID
	e.mu.Unlock()
	wailsruntime.EventsEmit(e.ctx, eventState, Status{State: state, Streams: streams, Total: total, ErrMsg: errMsg, ServerID: serverID})
}

func (e *Engine) OnLog(level, msg string, unixMillis int64) {
	if e.ctx == nil {
		return
	}
	wailsruntime.EventsEmit(e.ctx, eventLog, LogLine{Level: level, Msg: msg, AtMs: unixMillis})
}

func (e *Engine) OnCaptcha(url string) {
	if e.ctx == nil {
		return
	}
	wailsruntime.EventsEmit(e.ctx, eventCaptcha, url)
	if url != "" {
		go func() {
			_ = notify.ShowCaptchaNotification(url, nil)
		}()
	}
}
