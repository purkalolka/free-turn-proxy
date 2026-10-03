package serversetup

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// WireGuard users ("peers") on a server this app deployed. Every user is a
// separate WireGuard peer with its own key and tunnel address, plus an entry in
// the proxy's client allowlist (so the relay accepts them). The server-control
// script owns all of that (peer-add / peer-conf / peer-remove / share-list);
// this file is the typed client side of those commands.

// maxPeerNameRunes keeps the name (sent base64-encoded, capped at 256 chars by
// the script) comfortably inside that limit even for wide characters.
const maxPeerNameRunes = 40

// Peer is one WireGuard user as `share-list` reports it.
type Peer struct {
	PublicKey string `json:"pub"`
	// Name is empty for the owner and for peers this app did not create.
	Name string `json:"name"`
	IP   string `json:"ip"`
	// LastHandshake is when the user's client last completed a WireGuard
	// handshake, in unix seconds on the SERVER's clock; 0 means never. A client
	// that is connected re-handshakes about every two minutes, so this is the
	// best available "last seen".
	LastHandshake int64 `json:"lastHandshake"`
	// Rx/Tx are bytes received from / sent to this user since WireGuard last
	// started on the server (the kernel's counters reset with the interface).
	Rx int64 `json:"rx"`
	Tx int64 `json:"tx"`
	// HasConf: a stored config exists, so the connection can be shared again.
	HasConf bool `json:"hasConf"`
	IsOwner bool `json:"isOwner"`
}

// PeerList is one reading of the server's users.
type PeerList struct {
	Peers []Peer `json:"peers"`
	// ServerNow is the server's clock at the moment of the reading; "N minutes
	// ago" is computed against it, so a wrong clock on this PC can't skew it.
	ServerNow int64 `json:"serverNow"`
}

type shareListData struct {
	Peers []struct {
		Pub     string `json:"pub"`
		NameB64 string `json:"name_b64"`
		IP      string `json:"ip"`
		HS      int64  `json:"hs"`
		Rx      int64  `json:"rx"`
		Tx      int64  `json:"tx"`
		HasConf bool   `json:"has_conf"`
	} `json:"peers"`
	SelfPub string `json:"self_pub"`
	Now     int64  `json:"now"`
}

// ShareList reads the server's WireGuard users and when each last connected.
func ShareList(cfg SSHConfig) (*PeerList, error) {
	resp, _, err := runControl(cfg, []string{"share-list"})
	if err != nil {
		return nil, err
	}
	if !resp.IsOK() {
		return nil, controlErr(resp)
	}
	return parseShareList(resp.Data)
}

func parseShareList(raw json.RawMessage) (*PeerList, error) {
	var d shareListData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	out := &PeerList{Peers: make([]Peer, 0, len(d.Peers)), ServerNow: d.Now}
	for _, p := range d.Peers {
		out.Peers = append(out.Peers, Peer{
			PublicKey:     p.Pub,
			Name:          decodeName(p.NameB64),
			IP:            p.IP,
			LastHandshake: p.HS,
			Rx:            p.Rx,
			Tx:            p.Tx,
			HasConf:       p.HasConf,
			IsOwner:       d.SelfPub != "" && p.Pub == d.SelfPub,
		})
	}
	return out, nil
}

func decodeName(b64 string) string {
	if b64 == "" {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || !utf8.Valid(b) {
		return ""
	}
	return string(b)
}

// cleanPeerName validates a user-typed name: non-empty, one line, short.
func cleanPeerName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", errors.New("введите имя пользователя")
	}
	if n := utf8.RuneCountInString(name); n > maxPeerNameRunes {
		return "", fmt.Errorf("имя слишком длинное (%d символов, максимум %d)", n, maxPeerNameRunes)
	}
	return name, nil
}

// PeerIssued is a user's freshly issued (or re-issued) connection.
type PeerIssued struct {
	PublicKey string
	IP        string
	// ClientID is the proxy allowlist id tied to this user; it goes into their
	// share link so the relay accepts them.
	ClientID string
	// Conf is the user's WireGuard client config.
	Conf string
}

type peerAddData struct {
	Peer struct {
		Pub string `json:"pub"`
		IP  string `json:"ip"`
	} `json:"peer"`
	ClientID      string `json:"client_id"`
	ClientConfB64 string `json:"client_conf_b64"`
}

// PeerAdd creates a user: a new WireGuard peer plus a proxy allowlist entry for
// clientID. endpoint is written into the user's config (the local relay
// address, e.g. 127.0.0.1:9000 - irrelevant for the app's embedded tunnel, which
// ignores it, but needed if they use a separate WireGuard client).
//
// It runs as a detached server-side job: creating a peer is not idempotent, and
// the plain retry-on-dropped-connection path would happily create it twice.
func PeerAdd(cfg SSHConfig, name, endpoint, clientID string) (*PeerIssued, error) {
	name, err := cleanPeerName(name)
	if err != nil {
		return nil, err
	}
	argv := []string{
		"peer-add",
		"--name-b64=" + base64.StdEncoding.EncodeToString([]byte(name)),
		"--endpoint=" + endpoint,
		"--client-id=" + clientID,
	}
	resp, _, err := runControlDetached(cfg, argv, nil)
	if err != nil {
		return nil, err
	}
	if !resp.IsOK() {
		return nil, controlErr(resp)
	}
	var d peerAddData
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return nil, err
	}
	conf, err := decodeConf(d.ClientConfB64)
	if err != nil {
		return nil, err
	}
	return &PeerIssued{PublicKey: d.Peer.Pub, IP: d.Peer.IP, ClientID: d.ClientID, Conf: conf}, nil
}

type peerConfData struct {
	ClientID      string `json:"client_id"`
	ClientConfB64 string `json:"client_conf_b64"`
}

// PeerConf re-issues an existing user's stored config (to share it again).
//
// A user created before the proxy allowlist existed has no client id on the
// server; for those the script registers candidateClientID (with name, if
// given) and returns it, so an old user can still be shared. Both are ignored
// for users that already have one.
func PeerConf(cfg SSHConfig, pub, candidateClientID, name string) (*PeerIssued, error) {
	argv := []string{"peer-conf", "--pubkey=" + pub}
	if candidateClientID != "" {
		argv = append(argv, "--client-id="+candidateClientID)
		if name = strings.TrimSpace(name); name != "" {
			argv = append(argv, "--name-b64="+base64.StdEncoding.EncodeToString([]byte(name)))
		}
	}
	resp, _, err := runControl(cfg, argv)
	if err != nil {
		return nil, err
	}
	if !resp.IsOK() {
		return nil, controlErr(resp)
	}
	var d peerConfData
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return nil, err
	}
	conf, err := decodeConf(d.ClientConfB64)
	if err != nil {
		return nil, err
	}
	return &PeerIssued{PublicKey: pub, ClientID: d.ClientID, Conf: conf}, nil
}

// PeerRemove deletes a user: their WireGuard peer, stored config and proxy
// allowlist entry. Removing one that is already gone is not an error, so the
// call is safe to repeat. The owner's own peer is refused by the script.
func PeerRemove(cfg SSHConfig, pub string) error {
	resp, _, err := runControl(cfg, []string{"peer-remove", "--pubkey=" + pub})
	if err != nil {
		return err
	}
	if !resp.IsOK() {
		return controlErr(resp)
	}
	return nil
}

// ShareInfo is what the running server was actually started with - the truth
// for the obfuscation settings a user's client must match.
type ShareInfo struct {
	// Known is false when the server has no recorded start parameters (never
	// started by the app), in which case callers fall back to the saved profile.
	Known      bool
	Mode       string
	ObfProfile string
	ObfKey     string
}

type shareInfoData struct {
	Mode       string `json:"mode"`
	ObfProfile string `json:"obf_profile"`
	ObfKey     string `json:"obf_key"`
}

func GetShareInfo(cfg SSHConfig) (*ShareInfo, error) {
	resp, _, err := runControl(cfg, []string{"share-info"})
	if err != nil {
		return nil, err
	}
	if !resp.IsOK() {
		return nil, controlErr(resp)
	}
	var d shareInfoData
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return nil, err
	}
	return &ShareInfo{Known: d.ObfProfile != "", Mode: d.Mode, ObfProfile: d.ObfProfile, ObfKey: d.ObfKey}, nil
}

func decodeConf(b64 string) (string, error) {
	if b64 == "" {
		return "", errors.New("сервер не вернул конфиг пользователя")
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("конфиг пользователя повреждён: %w", err)
	}
	return string(b), nil
}
