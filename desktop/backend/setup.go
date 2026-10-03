package backend

import (
	"fmt"
	"strings"

	"github.com/samosvalishe/free-turn-proxy/desktop/backend/serversetup"
)

// ServerSetupDraft is the "deploy new VPS" wizard's config step, gathered
// before the install/wg-setup/start calls run.
type ServerSetupDraft struct {
	Name        string `json:"name"`
	VKLink      string `json:"vkLink"`
	ObfProfile  string `json:"obfProfile"`
	ObfKey      string `json:"obfKey"`
	ObfTimingMs int    `json:"obfTimingMs"`
	ListenPort  int    `json:"listenPort"`
	ClientID    string `json:"clientId"`
}

// BuildProvisionedServer assembles the saved profile for a VPS this app just
// provisioned - peer/obf/client-id match exactly what Start() was called
// with, and the WireGuard conf wg-setup returned is stored for reference.
func BuildProvisionedServer(ssh serversetup.SSHConfig, draft ServerSetupDraft, wg *serversetup.WgSetupResult) Server {
	srv := DefaultServer()
	srv.Name = draft.Name
	srv.Peer = fmt.Sprintf("%s:%d", ssh.IP, draft.ListenPort)
	if draft.VKLink != "" {
		srv.VKLinks = []string{draft.VKLink}
	}
	srv.ObfProfile = draft.ObfProfile
	srv.ObfKey = draft.ObfKey
	srv.ObfTimingMs = draft.ObfTimingMs
	if draft.ClientID != "" {
		srv.ClientID = draft.ClientID
	}
	sshCopy := ssh
	srv.SSH = &sshCopy
	if wg != nil {
		srv.WgClientConf = wg.ClientConf
		srv.WgPort = wg.Port
		// A conf came back, so the one-click embedded VPN is available - that's
		// what people expect "deploy a VPS" to give them, not a relay they then
		// have to wire into a separate WireGuard/AmneziaWG client by hand.
		srv.ConnMode = ConnModeVPN
	}
	return srv
}

// ListenPort extracts the port free-turn-proxy's server -listen used from a
// "host:port" Peer, so a restart can rebuild the same --listen flag.
func (s Server) ListenPort() string {
	i := strings.LastIndex(s.Peer, ":")
	if i < 0 {
		return ""
	}
	return s.Peer[i+1:]
}
