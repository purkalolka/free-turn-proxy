package backend

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"strings"
	"testing"

	"github.com/samosvalishe/free-turn-proxy/mobile"
)

// guestConf is what the server-control script writes for a new user
// (_wg_write_client_conf): comments-free but with MTU and blank lines.
func guestConf() string {
	return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.13.13.3/32\nMTU = 1280\nDNS = 1.1.1.1\n\n"+
		"[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = 127.0.0.1:9000\nPersistentKeepalive = 25\n",
		testKey(3), testKey(4))
}

func ownerServer() Server {
	s := DefaultServer()
	s.Name = "Германия"
	s.Peer = "203.0.113.5:56000"
	s.N, s.StreamsPerCred = 12, 12
	s.Listen = "127.0.0.1:9000"
	s.DNSMode, s.DNSServers = "doh", []string{"9.9.9.9"}
	s.ManualCaptcha = true
	s.VKLinks = []string{"https://vk.ru/call/join/owner"}
	return s
}

func TestGuestLinkContentAndNoOwnerLeaks(t *testing.T) {
	key := strings.Repeat("ab", 32)
	link, err := ownerServer().GuestLink(GuestLinkInput{
		ClientID: hexN(16), WgConf: guestConf(), Mode: "udp", ObfProfile: "rtpopus3", ObfKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseShareLink(link)
	if err != nil {
		t.Fatal(err)
	}

	if got.Provider != "vk" || got.Peer != "203.0.113.5:56000" || got.N != 12 || got.StreamsPerCred != 12 {
		t.Errorf("core fields wrong: %+v", got)
	}
	if got.Name != "Германия" {
		t.Errorf("name = %q, want the server's label", got.Name)
	}
	if got.ObfProfile != "rtpopus3" || got.ObfKey != key {
		t.Errorf("obfuscation = %q/%q, want the live server settings", got.ObfProfile, got.ObfKey)
	}
	if len(got.ClientID) != 32 {
		t.Errorf("client id = %q, want a 32-hex allowlist id", got.ClientID)
	}
	// The owner's own settings must not leak into a guest's profile.
	if got.Listen != "" || got.DNSMode != "" || len(got.DNSServers) != 0 || got.ManualCaptcha {
		t.Errorf("owner-local settings leaked into the link: %+v", got)
	}
	if got.VKLink != "" {
		t.Errorf("owner's VK call link included without being asked: %q", got.VKLink)
	}
	if strings.Contains(got.WgConf, "MTU") || strings.Contains(got.WgConf, "\n\n") || !strings.HasPrefix(got.WgConf, "[Interface]") {
		t.Errorf("wg conf not normalized:\n%s", got.WgConf)
	}
}

func TestGuestLinkIncludesVKOnlyOnRequest(t *testing.T) {
	link, err := ownerServer().GuestLink(GuestLinkInput{ClientID: hexN(16), WgConf: guestConf(), IncludeVK: true})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ParseShareLink(link)
	if got.VKLink != "https://vk.ru/call/join/owner" {
		t.Errorf("VK link = %q, want the owner's link when asked", got.VKLink)
	}
}

// What the server is really running beats what the saved profile says.
func TestGuestLinkPrefersLiveServerObfuscation(t *testing.T) {
	stale := ownerServer()
	stale.ObfProfile, stale.ObfKey = "rtpopus", strings.Repeat("11", 32)

	link, _ := stale.GuestLink(GuestLinkInput{ClientID: hexN(16), WgConf: guestConf(), Mode: "udp", ObfProfile: "rtpopus3", ObfKey: strings.Repeat("22", 32)})
	if got, _ := ParseShareLink(link); got.ObfProfile != "rtpopus3" || got.ObfKey != strings.Repeat("22", 32) {
		t.Errorf("used the stale saved profile: %q/%q", got.ObfProfile, got.ObfKey)
	}

	// Live server has obfuscation off: the link must say so, not fall back to the profile's.
	link, _ = stale.GuestLink(GuestLinkInput{ClientID: hexN(16), WgConf: guestConf(), Mode: "udp", ObfProfile: "none"})
	if got, _ := ParseShareLink(link); got.ObfProfile != "" || got.ObfKey != "" {
		t.Errorf("server runs without obfuscation but link carries %q/%q", got.ObfProfile, got.ObfKey)
	}

	// Nothing known from the server: fall back to the saved profile.
	link, _ = stale.GuestLink(GuestLinkInput{ClientID: hexN(16), WgConf: guestConf()})
	if got, _ := ParseShareLink(link); got.ObfProfile != "rtpopus" {
		t.Errorf("no live info should use the saved profile, got %q", got.ObfProfile)
	}
}

func TestGuestLinkNeverCarriesAnInvalidObfKey(t *testing.T) {
	link, _ := ownerServer().GuestLink(GuestLinkInput{ClientID: hexN(16), WgConf: guestConf(), Mode: "udp", ObfProfile: "rtpopus3", ObfKey: "not-hex"})
	if got, _ := ParseShareLink(link); got.ObfProfile != "" {
		t.Errorf("a broken key would make the guest's config invalid, but link has profile %q", got.ObfProfile)
	}
}

func TestGuestLinkTCPModeCarriesKCPOnlyWhenCustom(t *testing.T) {
	s := ownerServer()
	link, _ := s.GuestLink(GuestLinkInput{ClientID: hexN(16), WgConf: guestConf(), Mode: "tcp", ObfProfile: "none"})
	got, _ := ParseShareLink(link)
	if got.Mode != "tcp" || got.KCP != nil {
		t.Errorf("tcp/default kcp: mode=%q kcp=%v", got.Mode, got.KCP)
	}

	s.KCP.SndWnd = 1024
	link, _ = s.GuestLink(GuestLinkInput{ClientID: hexN(16), WgConf: guestConf(), Mode: "tcp", ObfProfile: "none"})
	got, _ = ParseShareLink(link)
	if got.KCP == nil || got.KCP.SndWnd != 1024 {
		t.Errorf("custom kcp lost: %+v", got.KCP)
	}
}

// End to end on the receiving side: importing the link gives a server that is
// one click from connecting - the conf must be accepted by the embedded tunnel.
func TestGuestLinkImportsIntoAWorkingProfile(t *testing.T) {
	link, err := ownerServer().GuestLink(GuestLinkInput{
		ClientID: hexN(16), WgConf: guestConf(), Mode: "udp", ObfProfile: "rtpopus3", ObfKey: strings.Repeat("cd", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseShareLink(link)
	if err != nil {
		t.Fatal(err)
	}
	base := DefaultServer()
	base.VKLinks = []string{"https://vk.ru/call/join/guest"} // the guest's own call
	srv := parsed.ApplyTo(base)

	if srv.ConnMode != ConnModeVPN || srv.WgClientConf == "" {
		t.Fatalf("imported profile is not VPN-ready: mode=%q conf=%q", srv.ConnMode, srv.WgClientConf)
	}
	if _, err := mobile.ParseTunnelConfig(srv.WgClientConf, DefaultTunnelMTU); err != nil {
		t.Fatalf("embedded tunnel rejects the normalized guest conf: %v", err)
	}
	cfg, err := srv.ToCoreConfig().JSON()
	if err != nil {
		t.Fatal(err)
	}
	if msg := mobile.ValidateConfig(cfg); msg != "" {
		t.Fatalf("core rejects the imported guest profile: %s", msg)
	}
}

func TestNormalizeWgConf(t *testing.T) {
	in := "[Interface]\r\n# comment\r\nPrivateKey = k\r\n; other\r\nmtu = 1280\r\n\r\nMTU=1300\r\nAddress = 10.0.0.2/32\r\n[Peer]\r\nEndpoint = 1.2.3.4:5\r\n"
	want := "[Interface]\nPrivateKey = k\nAddress = 10.0.0.2/32\n[Peer]\nEndpoint = 1.2.3.4:5"
	if got := NormalizeWgConf(in); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestQRDataURL(t *testing.T) {
	link, _ := ownerServer().GuestLink(GuestLinkInput{ClientID: hexN(16), WgConf: guestConf(), Mode: "udp", ObfProfile: "rtpopus3", ObfKey: strings.Repeat("ef", 32)})
	uri, err := QRDataURL(link)
	if err != nil {
		t.Fatalf("a realistic guest link (%d chars) must fit in a QR: %v", len(link), err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("not a valid PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() < 400 || b.Dx() != b.Dy() {
		t.Errorf("QR image is %dx%d, want a large square", b.Dx(), b.Dy())
	}
	t.Logf("link is %d chars", len(link))

	if _, err := QRDataURL(strings.Repeat("x", 4000)); err == nil {
		t.Error("an over-capacity payload should error (the UI then shows the link alone)")
	}
}

func hexN(n int) string { return GenerateClientID()[:n*2] }
