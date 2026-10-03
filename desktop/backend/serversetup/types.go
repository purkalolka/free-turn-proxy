package serversetup

import "encoding/json"

const (
	AuthPassword = "PASSWORD"
	AuthSSHKey   = "SSH_KEY"

	RootModeRoot       = "ROOT"
	RootModeSudoNoPass = "SUDO_NOPASS"
	RootModeSudoPass   = "SUDO_PASS"
)

// SSHConfig is how the app reaches a VPS to run the control script. RootMode
// and HostFingerprint are discovered during the wizard (DetectRootMode/Probe)
// and then carried forward into later calls for the same run.
type SSHConfig struct {
	IP              string `json:"ip"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	AuthType        string `json:"authType"`
	SSHKey          string `json:"sshKey"`
	HostFingerprint string `json:"hostFingerprint"`
	RootMode        string `json:"rootMode"`
	SudoPassword    string `json:"sudoPassword"`
}

// ControlResponse is the proto v2 envelope every control script run prints
// exactly once (see 10-proto.sh): result "ok" carries data, "err" carries code/msg/stage.
type ControlResponse struct {
	Proto  int             `json:"proto"`
	Result string          `json:"result"`
	Code   string          `json:"code"`
	Msg    string          `json:"msg"`
	Stage  string          `json:"stage"`
	Data   json.RawMessage `json:"data"`
	Logs   []string        `json:"logs"`
}

func (r *ControlResponse) IsOK() bool { return r != nil && r.Result == "ok" }

type wgInfo struct {
	Present bool `json:"present"`
	Port    *int `json:"port"`
}

type conflicts struct {
	Warp        bool     `json:"warp"`
	X3ui        bool     `json:"x3ui"`
	Wgeasy      bool     `json:"wgeasy"`
	Tailscale   bool     `json:"tailscale"`
	OtherIfaces []string `json:"other_ifaces"`
}

// probeData mirrors 80-cmd.sh's cmd_probe output.
type probeData struct {
	Installed bool      `json:"installed"`
	Version   string    `json:"version"`
	Running   bool      `json:"running"`
	Mode      string    `json:"mode"`
	Obf       string    `json:"obf"`
	Runtime   string    `json:"runtime"`
	Euid      int       `json:"euid"`
	Wg        wgInfo    `json:"wg"`
	Virt      string    `json:"virt"`
	WgKernel  bool      `json:"wg_kernel"`
	Conflicts conflicts `json:"conflicts"`
}

// installData mirrors cmd_install's output.
type installData struct {
	Stage        string `json:"stage"`
	Bin          string `json:"bin"`
	Version      string `json:"version"`
	Runtime      string `json:"runtime"`
	NeedsRestart bool   `json:"needs_restart"`
}

type wgSetupInfo struct {
	Port    int  `json:"port"`
	Existed bool `json:"existed"`
}

// wgSetupData mirrors cmd_wg_setup's output.
type wgSetupData struct {
	Wg            wgSetupInfo `json:"wg"`
	ClientConfB64 string      `json:"client_conf_b64"`
}
