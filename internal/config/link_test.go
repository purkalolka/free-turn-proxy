package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestClientJSONFromLink(t *testing.T) {
	payload := `{"v":1,"provider":"vk","peer":"1.2.3.4:56000","obf":"rtpopus3","key":"` + strings.Repeat("ab", 32) + `","wg":"[Interface]\nPrivateKey = x\nAddress = 10.0.0.2/32\n\n[Peer]\nPublicKey = y\nAllowedIPs = 0.0.0.0/0\nEndpoint = 127.0.0.1:1\n"}`
	link := "freeturn://" + base64.RawURLEncoding.EncodeToString([]byte(payload))

	js, err := ClientJSONFromLink(link, " https://vk.ru/call/join/abc ", "cid123")
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseClientJSON([]byte(js), "")
	if err != nil {
		t.Fatalf("generated config does not parse: %v\n%s", err, js)
	}
	if c.Proxy.Peer != "1.2.3.4:56000" || c.ClientID != "cid123" {
		t.Errorf("peer/clientID = %q/%q", c.Proxy.Peer, c.ClientID)
	}
	if !c.Tunnel.Enabled() || c.VK.Platform != PlatformMobile {
		t.Errorf("tunnel=%v platform=%v", c.Tunnel.Enabled(), c.VK.Platform)
	}
	if len(c.VK.Links) != 1 || c.VK.Links[0] != "abc" {
		t.Errorf("links = %v", c.VK.Links)
	}

	if _, err := ClientJSONFromLink(link, "", "cid"); err == nil {
		t.Error("expected error for empty vk link")
	}
	if _, err := ClientJSONFromLink("vless://x", "https://vk.ru/call/join/abc", "cid"); err == nil {
		t.Error("expected error for wrong scheme")
	}
}
