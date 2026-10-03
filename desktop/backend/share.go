package backend

import (
	"encoding/base64"
	"regexp"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// GuestShare is everything the UI needs to hand a user their connection: a
// freeturn:// link and the same link as a QR code.
type GuestShare struct {
	Pub  string `json:"pub"`
	Name string `json:"name"`
	IP   string `json:"ip"`
	Link string `json:"link"`
	// QR is a PNG data URL of Link, empty if the link is too long to encode.
	QR string `json:"qr"`
	// IncludesVK reports whether the owner's own call link is inside the link.
	IncludesVK bool `json:"includesVk"`
}

// GuestLinkInput is what goes into one user's link besides the server profile.
type GuestLinkInput struct {
	// ClientID is the user's proxy allowlist id; WgConf their WireGuard config.
	ClientID string
	WgConf   string
	// IncludeVK puts the owner's own VK call link into the link. Off by default:
	// a call link is normally per-person, so the recipient enters their own.
	IncludeVK bool
	// The obfuscation the server is ACTUALLY running with (from share-info).
	// The user's client must match it exactly; empty ObfProfile means "unknown,
	// use the saved profile".
	Mode, ObfProfile, ObfKey string
}

var obfKeyRe = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// GuestLink builds the freeturn:// link a user imports, following the Android
// app's ShareLinkBuilder so links made here and there are interchangeable. The
// owner's local settings (listen address, DNS, captcha mode) deliberately stay
// out: they are the owner's, not the server's.
func (s Server) GuestLink(in GuestLinkInput) (string, error) {
	mode, obfProfile, obfKey := s.Mode, s.ObfProfile, s.ObfKey
	if in.ObfProfile != "" {
		mode, obfProfile, obfKey = in.Mode, in.ObfProfile, in.ObfKey
	}

	link := ShareLink{
		Provider:       s.Provider,
		Peer:           s.Peer,
		N:              s.N,
		StreamsPerCred: s.StreamsPerCred,
		ClientID:       in.ClientID,
		Name:           strings.TrimSpace(s.Name),
		WgConf:         NormalizeWgConf(in.WgConf),
	}
	if s.Transport == "udp" {
		link.Transport = "udp"
	}
	if mode == "tcp" {
		link.Mode = "tcp"
		// The ARQ profile isn't reported by the server: pass what the owner set.
		if s.KCP != DefaultKCPProfile() {
			kcp := s.KCP
			link.KCP = &kcp
		}
	}
	if obfProfile != "" && obfProfile != "none" && obfKeyRe.MatchString(obfKey) {
		link.ObfProfile, link.ObfKey = obfProfile, obfKey
	}
	if in.IncludeVK && len(s.VKLinks) > 0 {
		link.VKLink = strings.TrimSpace(s.VKLinks[0])
	}
	return link.Encode()
}

// NormalizeWgConf strips comments, blank lines and MTU from a WireGuard config:
// a shorter link makes a less dense QR, and the recipient substitutes their own
// MTU (a constant of the tunnel), same as the Android app does.
func NormalizeWgConf(conf string) string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(conf, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(t), "mtu") && strings.Contains(t, "=") {
			continue
		}
		out = append(out, t)
	}
	return strings.Join(out, "\n")
}

// QRDataURL renders content as a QR code PNG data URL. Low error correction on
// purpose: the code is shown on a screen, not printed and scuffed, and every
// step down in redundancy makes the (already dense) code much easier to scan.
func QRDataURL(content string) (string, error) {
	png, err := qrcode.Encode(content, qrcode.Low, 640)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}
