// Package backend builds engine configs, persists server profiles and drives
// the in-process free-turn-proxy core (github.com/samosvalishe/free-turn-proxy/mobile).
package backend

import "encoding/json"

// KCPProfile mirrors internal/config's kcpJSON tags (camelCase) - used both as the
// wire "kcp" section of CoreConfig and as the stored per-server ARQ profile.
type KCPProfile struct {
	NoDelay    int  `json:"noDelay"`
	Interval   int  `json:"interval"`
	Resend     int  `json:"resend"`
	NC         int  `json:"nc"`
	SndWnd     int  `json:"sndWnd"`
	RcvWnd     int  `json:"rcvWnd"`
	MTU        int  `json:"mtu"`
	ACKNoDelay bool `json:"ackNoDelay"`
}

// DefaultKCPProfile matches kcpmux.DefaultProfile() in the core.
func DefaultKCPProfile() KCPProfile {
	return KCPProfile{NoDelay: 1, Interval: 20, Resend: 2, NC: 1, SndWnd: 512, RcvWnd: 512, MTU: 1200, ACKNoDelay: true}
}

// CoreConfig mirrors internal/config.ClientJSON - the JSON schema accepted by
// mobile.Start/mobile.ValidateConfig/mobile.ConfigToArgs. Field tags must match
// that schema exactly; the core rejects unknown fields.
type CoreConfig struct {
	Peer     string     `json:"peer"`
	ClientID string     `json:"clientId"`
	SubURL   string     `json:"subUrl"`
	Provider string     `json:"provider"`
	Routes   bool       `json:"routes"`
	TURN     CoreTurn   `json:"turn"`
	Proxy    CoreProxy  `json:"proxy"`
	VK       CoreVK     `json:"vk"`
	Obf      CoreObf    `json:"obf"`
	DNS      CoreDNS    `json:"dns"`
	Log      CoreLog    `json:"log"`
	KCP      KCPProfile `json:"kcp"`
	Tunnel   CoreTunnel `json:"tunnel"`
}

type CoreTurn struct {
	N         int    `json:"n"`
	Transport string `json:"transport"`
	Host      string `json:"host"`
	Port      string `json:"port"`
}

type CoreProxy struct {
	Mode   string `json:"mode"`
	Listen string `json:"listen"`
}

type CoreVK struct {
	Links          []string `json:"links"`
	StreamsPerCred int      `json:"streamsPerCred"`
	ManualCaptcha  bool     `json:"manualCaptcha"`
	Platform       string   `json:"platform"`
}

type CoreObf struct {
	Profile  string `json:"profile"`
	Key      string `json:"key"`
	TimingMs int    `json:"timingMs"`
}

type CoreDNS struct {
	Mode    string   `json:"mode"`
	Servers []string `json:"servers"`
}

type CoreLog struct {
	Debug bool `json:"debug"`
}

// CoreTunnel is the embedded userspace WireGuard tunnel section - populated
// only in ConnModeVPN. "wg" (not "awg"): the disguise already happens one
// layer down, in the TURN relay's obfuscation profile, so the WG layer itself
// runs plain (matches how the server-control script's wg-setup provisions it).
type CoreTunnel struct {
	Mode   string `json:"mode"`
	Config string `json:"config"`
	MTU    int    `json:"mtu"`
}

// DefaultTunnelMTU matches the server-control script's WG_MTU: the tunnel
// rides over TURN + DTLS + obfuscation overhead, so the usual 1420 doesn't fit.
const DefaultTunnelMTU = 1280

// JSON renders the config exactly as mobile.Start expects it.
func (c CoreConfig) JSON() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ToCoreConfig maps a saved server profile onto the engine's wire schema.
func (s Server) ToCoreConfig() CoreConfig {
	return CoreConfig{
		Peer:     s.Peer,
		ClientID: s.ClientID,
		Provider: s.Provider,
		Routes:   s.Routes,
		TURN: CoreTurn{
			N:         s.N,
			Transport: s.Transport,
			Host:      s.TurnHost,
			Port:      s.TurnPort,
		},
		Proxy: CoreProxy{Mode: s.Mode, Listen: s.Listen},
		VK: CoreVK{
			Links:          s.VKLinks,
			StreamsPerCred: s.StreamsPerCred,
			ManualCaptcha:  s.ManualCaptcha,
			Platform:       "desktop",
		},
		Obf: CoreObf{
			Profile:  s.ObfProfile,
			Key:      s.ObfKey,
			TimingMs: s.ObfTimingMs,
		},
		DNS:    CoreDNS{Mode: s.DNSMode, Servers: s.DNSServers},
		Log:    CoreLog{Debug: s.Debug},
		KCP:    s.KCP,
		Tunnel: s.tunnelConfig(),
	}
}

func (s Server) tunnelConfig() CoreTunnel {
	if s.ConnMode != ConnModeVPN || s.WgClientConf == "" {
		return CoreTunnel{}
	}
	return CoreTunnel{Mode: "wg", Config: s.WgClientConf, MTU: DefaultTunnelMTU}
}
