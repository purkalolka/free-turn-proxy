package backend

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/samosvalishe/free-turn-proxy/mobile"
)

func testKey(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

// androidWgConf is what the Android app's ShareLinkBuilder.normalizeConf emits:
// trimmed lines, no comments, no blank lines, no MTU. (All-zero keys are
// rejected by the parser as "unset", hence non-zero test keys.)
var androidWgConf = fmt.Sprintf("[Interface]\n"+
	"PrivateKey = %s\n"+
	"Address = 10.112.83.104/32\n"+
	"DNS = 1.1.1.1\n"+
	"[Peer]\n"+
	"PublicKey = %s\n"+
	"AllowedIPs = 0.0.0.0/0, ::/0\n"+
	"Endpoint = 127.0.0.1:9000", testKey(1), testKey(2))

func androidStyleLink(t *testing.T, wg string) string {
	t.Helper()
	m := map[string]any{"v": 1, "provider": "vk", "peer": "203.0.113.5:56000", "cid": "abc123", "name": "Guest"}
	if wg != "" {
		m["wg"] = wg
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return linkScheme + base64.RawURLEncoding.EncodeToString(raw)
}

func TestImportLinkWithWgConfSwitchesToVPN(t *testing.T) {
	link, err := ParseShareLink(androidStyleLink(t, androidWgConf))
	if err != nil {
		t.Fatal(err)
	}
	if link.WgConf != androidWgConf {
		t.Fatalf("WgConf lost or mangled: %q", link.WgConf)
	}

	// The import modal fills in the guest's own call link before applying.
	base := DefaultServer()
	base.VKLinks = []string{"https://vk.ru/call/join/abc"}
	srv := link.ApplyTo(base)
	if srv.ConnMode != ConnModeVPN || srv.WgClientConf != androidWgConf {
		t.Fatalf("want VPN mode with conf, got mode=%q conf=%q", srv.ConnMode, srv.WgClientConf)
	}

	if _, err := mobile.ParseTunnelConfig(srv.WgClientConf, DefaultTunnelMTU); err != nil {
		t.Fatalf("Android-style conf must parse in the embedded tunnel: %v", err)
	}
	cfg, err := srv.ToCoreConfig().JSON()
	if err != nil {
		t.Fatal(err)
	}
	if msg := mobile.ValidateConfig(cfg); msg != "" {
		t.Fatalf("core rejects imported profile: %s", msg)
	}
}

func TestImportLinkWithoutWgKeepsRelayAndExistingConf(t *testing.T) {
	link, err := ParseShareLink(androidStyleLink(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := link.ApplyTo(DefaultServer()); got.ConnMode != ConnModeRelay || got.WgClientConf != "" {
		t.Fatalf("no wg in link must stay relay, got mode=%q conf=%q", got.ConnMode, got.WgClientConf)
	}

	existing := DefaultServer()
	existing.WgClientConf, existing.ConnMode = androidWgConf, ConnModeVPN
	if got := link.ApplyTo(existing); got.ConnMode != ConnModeVPN || got.WgClientConf != androidWgConf {
		t.Fatalf("link without wg must not clobber an existing conf, got mode=%q", got.ConnMode)
	}
}

func TestShareLinkWgRoundTrip(t *testing.T) {
	encoded, err := ShareLink{Provider: "vk", Peer: "203.0.113.5:56000", WgConf: androidWgConf}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseShareLink(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if back.WgConf != androidWgConf {
		t.Fatalf("round trip lost wg conf: %q", back.WgConf)
	}
}
