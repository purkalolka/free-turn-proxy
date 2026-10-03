package backend

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/samosvalishe/free-turn-proxy/desktop/backend/serversetup"
)

// Server is a saved connection profile - one VK-call relay + VPS peer setup
// the user can pick from the server list and connect to.
type Server struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Peer     string   `json:"peer"`
	Provider string   `json:"provider"`
	VKLinks  []string `json:"vkLinks"`

	Transport string `json:"transport"`
	Mode      string `json:"mode"`

	ObfProfile  string `json:"obfProfile"`
	ObfKey      string `json:"obfKey"`
	ObfTimingMs int    `json:"obfTimingMs"`

	N              int    `json:"n"`
	StreamsPerCred int    `json:"streamsPerCred"`
	ClientID       string `json:"clientId"`
	Listen         string `json:"listen"`

	DNSMode    string   `json:"dnsMode"`
	DNSServers []string `json:"dnsServers"`

	ManualCaptcha bool `json:"manualCaptcha"`
	Debug         bool `json:"debug"`
	Routes        bool `json:"routes"`

	// TurnHost/TurnPort override the TURN server address from provider creds; empty uses the creds as-is.
	TurnHost string     `json:"turnHost"`
	TurnPort string     `json:"turnPort"`
	KCP      KCPProfile `json:"kcp"`

	// SSH is set only for servers this app provisioned itself (see the "deploy
	// new VPS" wizard); nil for servers added by link or manual entry.
	SSH *serversetup.SSHConfig `json:"ssh,omitempty"`
	// WgClientConf is the WireGuard config wg-setup generated (Endpoint already
	// pointed at Listen) - import it into a separate WireGuard/AmneziaWG client.
	WgClientConf string `json:"wgClientConf,omitempty"`
	// WgPort is the VPS-local port the managed WireGuard backend listens on -
	// used as the free-turn-proxy server's -connect target on restart.
	WgPort int `json:"wgPort,omitempty"`

	// ConnMode picks how Connect wires this server up: ConnModeRelay (default -
	// listens locally, a separate WireGuard/AmneziaWG client points at it) or
	// ConnModeVPN (embedded tunnel - Connect creates the OS network adapter
	// itself from WgClientConf, no separate client needed). VPN mode requires
	// WgClientConf to be set.
	ConnMode string `json:"connMode,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
}

const (
	ConnModeRelay = "relay"
	ConnModeVPN   = "vpn"
)

// DefaultServer returns a new profile pre-filled with the core's own client defaults.
func DefaultServer() Server {
	return Server{
		Provider:       "vk",
		VKLinks:        []string{},
		Transport:      "tcp",
		Mode:           "udp",
		ObfProfile:     "rtpopus3",
		N:              12,
		StreamsPerCred: 12,
		Listen:         "127.0.0.1:9000",
		DNSMode:        "auto",
		DNSServers:     []string{},
		ClientID:       generateHex(16),
		KCP:            DefaultKCPProfile(),
		ConnMode:       ConnModeRelay,
	}
}

type Store struct {
	mu      sync.Mutex
	path    string
	servers []Server
}

func NewStore() (*Store, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve config dir: %w", err)
	}
	appDir := filepath.Join(dir, "FreeTurnDesktop")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	s := &Store{path: filepath.Join(appDir, "servers.json")}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// StateDir is where the core keeps its own state between runs (VK persona, TURN cred cache).
func (s *Store) StateDir() string {
	return filepath.Join(filepath.Dir(s.path), "core-state")
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		s.servers = []Server{}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read servers file: %w", err)
	}
	var list []Server
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("parse servers file: %w", err)
	}
	for i := range list {
		normalizeServer(&list[i])
	}
	s.servers = list
	return nil
}

// normalizeServer heals profiles saved before DefaultServer() always set
// VKLinks/DNSServers: a nil slice marshals as JSON null, which the frontend
// (fields like `vkLinks.join(...)`) isn't written to expect. It also backfills
// ConnMode for profiles saved before that field existed: a server with a
// generated WireGuard conf (the "deploy new VPS" wizard) defaults to the
// one-click embedded VPN instead of the older relay-only mode.
func normalizeServer(s *Server) {
	if s.VKLinks == nil {
		s.VKLinks = []string{}
	}
	if s.DNSServers == nil {
		s.DNSServers = []string{}
	}
	if s.ConnMode == "" {
		if s.WgClientConf != "" {
			s.ConnMode = ConnModeVPN
		} else {
			s.ConnMode = ConnModeRelay
		}
	}
}

// persist writes to a temp file and renames over the target so a crash mid-write
// never leaves servers.json truncated.
func (s *Store) persist() error {
	data, err := json.MarshalIndent(s.servers, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) List() []Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Server, len(s.servers))
	copy(out, s.servers)
	return out
}

func (s *Store) Get(id string) (Server, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, srv := range s.servers {
		if srv.ID == id {
			return srv, true
		}
	}
	return Server{}, false
}

// Save inserts or updates a profile by ID; a blank ID is treated as a new profile.
func (s *Store) Save(srv Server) (Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalizeServer(&srv)
	if srv.ID == "" {
		srv.ID = generateHex(8)
		srv.CreatedAt = time.Now()
		s.servers = append(s.servers, srv)
	} else {
		found := false
		for i, existing := range s.servers {
			if existing.ID == srv.ID {
				srv.CreatedAt = existing.CreatedAt
				s.servers[i] = srv
				found = true
				break
			}
		}
		if !found {
			srv.CreatedAt = time.Now()
			s.servers = append(s.servers, srv)
		}
	}
	if err := s.persist(); err != nil {
		return Server{}, err
	}
	return srv, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.servers[:0]
	for _, srv := range s.servers {
		if srv.ID != id {
			out = append(out, srv)
		}
	}
	s.servers = out
	return s.persist()
}

// GenerateClientID returns a fresh 32-hex-char client ID (16 random bytes),
// matching the core's own client-id format.
func GenerateClientID() string { return generateHex(16) }

func generateHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing means the OS entropy source is broken - a
		// timestamp-derived fallback keeps profile creation from hard-failing.
		for i := range b {
			b[i] = byte(time.Now().UnixNano() >> uint(i))
		}
	}
	return fmt.Sprintf("%x", b)
}
