package serversetup

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func shareListEnvelope(t *testing.T) string {
	t.Helper()
	data := map[string]any{
		"peers": []map[string]any{
			{"pub": "OWNERPUBKEYOWNERPUBKEYOWNERPUBKEYOWNERPUBK=", "ip": "10.13.13.2", "hs": 1700000100, "rx": 5000, "tx": 9000, "has_conf": false},
			{"pub": "ALICEPUBKEYALICEPUBKEYALICEPUBKEYALICEPUBK=", "name_b64": b64("Вася 🙂"), "ip": "10.13.13.3", "hs": 1700000190, "rx": 123456789, "tx": 987654, "has_conf": true},
			{"pub": "BOBPUBKEYBOBPUBKEYBOBPUBKEYBOBPUBKEYBOBPUBK=", "name_b64": b64("Bob"), "ip": "10.13.13.4", "has_conf": true},
		},
		"now":      1700000200,
		"self_pub": "OWNERPUBKEYOWNERPUBKEYOWNERPUBKEYOWNERPUBK=",
		"clients":  []any{},
	}
	raw, err := json.Marshal(map[string]any{"proto": 2, "result": "ok", "data": data, "logs": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw) + "\n"
}

func TestShareListParsing(t *testing.T) {
	srv := newFakeSSHServer(t, func(string, []byte) (string, int) { return shareListEnvelope(t), 0 })
	list, err := ShareList(srv.sshConfig())
	if err != nil {
		t.Fatal(err)
	}
	if list.ServerNow != 1700000200 || len(list.Peers) != 3 {
		t.Fatalf("list = %+v", list)
	}

	owner, alice, bob := list.Peers[0], list.Peers[1], list.Peers[2]
	if !owner.IsOwner || alice.IsOwner || bob.IsOwner {
		t.Errorf("owner detection wrong: %v %v %v", owner.IsOwner, alice.IsOwner, bob.IsOwner)
	}
	if alice.Name != "Вася 🙂" || alice.IP != "10.13.13.3" || !alice.HasConf {
		t.Errorf("alice = %+v (unicode name must survive base64)", alice)
	}
	if alice.LastHandshake != 1700000190 || alice.Rx != 123456789 || alice.Tx != 987654 {
		t.Errorf("alice activity = %+v", alice)
	}
	if bob.LastHandshake != 0 {
		t.Errorf("a user who never connected must have no handshake, got %d", bob.LastHandshake)
	}
	if owner.Name != "" {
		t.Errorf("owner has no app-assigned name, got %q", owner.Name)
	}
}

func TestParseShareListToleratesBadName(t *testing.T) {
	raw := json.RawMessage(`{"peers":[{"pub":"K","name_b64":"!!!not-base64!!!","has_conf":true}],"now":5}`)
	list, err := parseShareList(raw)
	if err != nil || len(list.Peers) != 1 || list.Peers[0].Name != "" {
		t.Fatalf("a corrupt name must degrade to empty, not fail the whole list: %+v %v", list, err)
	}
}

func TestCleanPeerName(t *testing.T) {
	if n, err := cleanPeerName("  Вася   Иванов \n"); err != nil || n != "Вася Иванов" {
		t.Errorf("got %q, %v", n, err)
	}
	if _, err := cleanPeerName("   "); err == nil {
		t.Error("empty name accepted")
	}
	if _, err := cleanPeerName(strings.Repeat("я", maxPeerNameRunes+1)); err == nil {
		t.Error("over-long name accepted")
	}
	// Wide characters are 4 bytes each: the base64 must still fit the script's 256-char cap.
	if n, _ := cleanPeerName(strings.Repeat("🙂", maxPeerNameRunes)); len(b64(n)) > 256 {
		t.Errorf("a maximal name encodes to %d chars, over the script's 256 limit", len(b64(n)))
	}
}

// Public keys contain '+' and '/': they must reach the script as one intact
// argument, not be split or expanded by the remote shell.
func TestPeerCommandsQuoteKeysSafely(t *testing.T) {
	const pub = "a+b/cDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefg="
	var mu sync.Mutex
	var cmds []string
	srv := newFakeSSHServer(t, func(cmd string, _ []byte) (string, int) {
		mu.Lock()
		cmds = append(cmds, cmd)
		mu.Unlock()
		if strings.Contains(cmd, "peer-conf") {
			return `{"proto":2,"result":"ok","data":{"client_id":"` + strings.Repeat("a", 32) + `","client_conf_b64":"` + b64("[Interface]") + `"},"logs":[]}` + "\n", 0
		}
		return `{"proto":2,"result":"ok","data":{"removed":true},"logs":[]}` + "\n", 0
	})
	cfg := srv.sshConfig()

	if err := PeerRemove(cfg, pub); err != nil {
		t.Fatal(err)
	}
	if _, err := PeerConf(cfg, pub, "", ""); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, c := range cmds {
		if !strings.Contains(c, "'--pubkey="+pub+"'") {
			t.Errorf("key not passed as one quoted argument: %s", c)
		}
	}
}

func TestPeerConfSendsCandidateClientIDOnlyWhenGiven(t *testing.T) {
	var mu sync.Mutex
	var last string
	srv := newFakeSSHServer(t, func(cmd string, _ []byte) (string, int) {
		mu.Lock()
		last = cmd
		mu.Unlock()
		return `{"proto":2,"result":"ok","data":{"client_conf_b64":"` + b64("[Interface]") + `"},"logs":[]}` + "\n", 0
	})
	cfg := srv.sshConfig()
	cid := strings.Repeat("b", 32)

	if _, err := PeerConf(cfg, "KEY=", cid, "Вася"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	withCID := last
	mu.Unlock()
	if !strings.Contains(withCID, "--client-id="+cid) || !strings.Contains(withCID, "--name-b64=") {
		t.Errorf("candidate id/name not sent: %s", withCID)
	}

	if _, err := PeerConf(cfg, "KEY=", "", "Вася"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(last, "--client-id") || strings.Contains(last, "--name-b64") {
		t.Errorf("no candidate given, yet extra args sent: %s", last)
	}
}

func TestPeerRemoveSurfacesScriptRefusal(t *testing.T) {
	srv := newFakeSSHServer(t, func(string, []byte) (string, int) {
		return `{"proto":2,"result":"err","code":"bad_arg","msg":"cannot remove owner peer","stage":"peer_remove","logs":[]}` + "\n", 1
	})
	err := PeerRemove(srv.sshConfig(), "KEY=")
	if err == nil || !strings.Contains(err.Error(), "cannot remove owner peer") {
		t.Fatalf("err = %v, want the script's reason", err)
	}
}
