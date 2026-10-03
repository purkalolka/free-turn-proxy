package backend

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	linkScheme  = "freeturn://"
	linkVersion = 1
)

// linkKCP mirrors the *lowercase* json tags free-turn-proxy's internal/uri.KCP
// uses on the wire - distinct from KCPProfile's camelCase config-JSON tags.
type linkKCP struct {
	NoDelay    int  `json:"nodelay"`
	Interval   int  `json:"interval"`
	Resend     int  `json:"resend"`
	NC         int  `json:"nc"`
	SndWnd     int  `json:"sndwnd"`
	RcvWnd     int  `json:"rcvwnd"`
	MTU        int  `json:"mtu"`
	ACKNoDelay bool `json:"acknodelay"`
}

// linkPayload is the base64url(json) payload of a freeturn:// link, per docs/uri.md.
type linkPayload struct {
	V              int      `json:"v"`
	Provider       string   `json:"provider"`
	Peer           string   `json:"peer"`
	Transport      string   `json:"transport,omitempty"`
	Mode           string   `json:"mode,omitempty"`
	Obf            string   `json:"obf,omitempty"`
	Key            string   `json:"key,omitempty"`
	N              int      `json:"n,omitempty"`
	StreamsPerCred int      `json:"spc,omitempty"`
	ClientID       string   `json:"cid,omitempty"`
	Listen         string   `json:"listen,omitempty"`
	DNSMode        string   `json:"dns,omitempty"`
	DNSServers     string   `json:"dnss,omitempty"`
	ManualCaptcha  bool     `json:"mcap,omitempty"`
	KCP            *linkKCP `json:"kcp,omitempty"`
	Name           string   `json:"name,omitempty"`
	// VKLink is not part of the core spec (owner links normally omit -link since
	// it is per-guest) but mirrors the Android app's convenience "vk" field for
	// when an owner shares their own still-open call link too.
	VKLink string `json:"vk,omitempty"`
	// WgConf is the Android app's "wg" extension: the guest's own WireGuard
	// client config (a separate peer the owner created for them), so importing
	// the link is enough to bring up the embedded VPN with no separate client.
	WgConf string `json:"wg,omitempty"`
}

// ShareLink is the decoded/editable form of a freeturn:// link.
type ShareLink struct {
	Provider       string      `json:"provider"`
	Peer           string      `json:"peer"`
	Transport      string      `json:"transport"`
	Mode           string      `json:"mode"`
	ObfProfile     string      `json:"obfProfile"`
	ObfKey         string      `json:"obfKey"`
	N              int         `json:"n"`
	StreamsPerCred int         `json:"streamsPerCred"`
	ClientID       string      `json:"clientId"`
	Listen         string      `json:"listen"`
	DNSMode        string      `json:"dnsMode"`
	DNSServers     []string    `json:"dnsServers"`
	ManualCaptcha  bool        `json:"manualCaptcha"`
	KCP            *KCPProfile `json:"kcp,omitempty"`
	Name           string      `json:"name"`
	VKLink         string      `json:"vkLink"`
	WgConf         string      `json:"wgConf"`
}

// LooksLikeLink reports whether raw is (trimmed of whitespace) a freeturn:// link.
func LooksLikeLink(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), linkScheme)
}

func ParseShareLink(raw string) (*ShareLink, error) {
	trimmed := strings.TrimSpace(raw)
	if !LooksLikeLink(trimmed) {
		return nil, errors.New("invalid scheme")
	}
	payload := trimmed[len(linkScheme):]
	if payload == "" {
		return nil, errors.New("empty payload")
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("decode link: %w", err)
	}
	var p linkPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse link json: %w", err)
	}
	if p.V != linkVersion {
		return nil, fmt.Errorf("unsupported link version %d", p.V)
	}
	if p.Provider == "" || p.Peer == "" {
		return nil, errors.New("link missing provider/peer")
	}

	link := &ShareLink{
		Provider:       p.Provider,
		Peer:           p.Peer,
		Transport:      p.Transport,
		Mode:           p.Mode,
		ObfProfile:     p.Obf,
		ObfKey:         p.Key,
		N:              p.N,
		StreamsPerCred: p.StreamsPerCred,
		ClientID:       p.ClientID,
		Listen:         p.Listen,
		DNSMode:        p.DNSMode,
		ManualCaptcha:  p.ManualCaptcha,
		Name:           p.Name,
		VKLink:         p.VKLink,
		WgConf:         strings.TrimSpace(p.WgConf),
	}
	if p.DNSServers != "" {
		link.DNSServers = splitAndTrim(p.DNSServers)
	}
	if p.KCP != nil {
		link.KCP = &KCPProfile{
			NoDelay: p.KCP.NoDelay, Interval: p.KCP.Interval, Resend: p.KCP.Resend, NC: p.KCP.NC,
			SndWnd: p.KCP.SndWnd, RcvWnd: p.KCP.RcvWnd, MTU: p.KCP.MTU, ACKNoDelay: p.KCP.ACKNoDelay,
		}
	}
	return link, nil
}

// Encode renders the link as freeturn://base64url(json), omitting empty/default fields.
func (l ShareLink) Encode() (string, error) {
	p := linkPayload{
		V: linkVersion, Provider: l.Provider, Peer: l.Peer,
		Transport: l.Transport, Mode: l.Mode,
		N: l.N, StreamsPerCred: l.StreamsPerCred, ClientID: l.ClientID,
		Listen: l.Listen, DNSMode: l.DNSMode,
		ManualCaptcha: l.ManualCaptcha, Name: l.Name, VKLink: l.VKLink, WgConf: l.WgConf,
	}
	if l.ObfProfile != "" && l.ObfProfile != "none" {
		p.Obf = l.ObfProfile
		p.Key = l.ObfKey
	}
	if len(l.DNSServers) > 0 {
		p.DNSServers = strings.Join(l.DNSServers, ",")
	}
	if l.KCP != nil {
		p.KCP = &linkKCP{
			NoDelay: l.KCP.NoDelay, Interval: l.KCP.Interval, Resend: l.KCP.Resend, NC: l.KCP.NC,
			SndWnd: l.KCP.SndWnd, RcvWnd: l.KCP.RcvWnd, MTU: l.KCP.MTU, ACKNoDelay: l.KCP.ACKNoDelay,
		}
	}
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return linkScheme + base64.RawURLEncoding.EncodeToString(data), nil
}

// ApplyTo overlays the link's fields onto an existing server profile (fields the
// link leaves empty/zero keep the profile's current value), mirroring how a guest
// merges an owner's share link into their own local profile.
func (l ShareLink) ApplyTo(s Server) Server {
	s.Provider = l.Provider
	s.Peer = l.Peer
	if l.Transport != "" {
		s.Transport = l.Transport
	}
	if l.Mode != "" {
		s.Mode = l.Mode
	}
	// Obfuscation must match the server exactly, so the link is authoritative:
	// an owner running without it omits obf/key (as the Android builder does),
	// which means "none" - not "keep the local default", which is a keyed
	// profile and would leave the imported server with an invalid config.
	if l.ObfProfile != "" {
		s.ObfProfile = l.ObfProfile
		s.ObfKey = l.ObfKey
	} else {
		s.ObfProfile = "none"
		s.ObfKey = ""
	}
	if l.N != 0 {
		s.N = l.N
	}
	if l.StreamsPerCred != 0 {
		s.StreamsPerCred = l.StreamsPerCred
	}
	if l.ClientID != "" {
		s.ClientID = l.ClientID
	}
	if l.Listen != "" {
		s.Listen = l.Listen
	}
	if l.DNSMode != "" {
		s.DNSMode = l.DNSMode
	}
	if len(l.DNSServers) > 0 {
		s.DNSServers = l.DNSServers
	}
	s.ManualCaptcha = l.ManualCaptcha
	if l.KCP != nil {
		s.KCP = *l.KCP
	}
	if l.Name != "" {
		s.Name = l.Name
	}
	if l.VKLink != "" {
		s.VKLinks = []string{l.VKLink}
	}
	if l.WgConf != "" {
		s.WgClientConf = l.WgConf
		s.ConnMode = ConnModeVPN
	}
	return s
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
