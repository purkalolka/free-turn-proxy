package serversetup

import (
	"strings"
	"sync/atomic"
	"testing"
)

const okEnvelope = `{"proto":2,"result":"ok","data":{"installed":true},"logs":["a"]}`

func TestParseEnvelope(t *testing.T) {
	cases := []struct {
		name, out string
		wantOK    bool
	}{
		{"clean", okEnvelope + "\n", true},
		{"noise before (sudo warning)", "sudo: unable to resolve host vps1: Name or service not known\n" + okEnvelope + "\n", true},
		{"noise after", okEnvelope + "\nlogout\n", true},
		{"truncated json", `{"proto":2,"result":"ok","data":{"inst`, false},
		{"not the protocol", `{"hello":"world"}` + "\n", false},
		{"empty", "  \n", false},
	}
	for _, c := range cases {
		resp, err := parseEnvelope(c.out)
		if (err == nil) != c.wantOK {
			t.Errorf("%s: err = %v, wantOK = %v", c.name, err, c.wantOK)
			continue
		}
		if c.wantOK && !resp.IsOK() {
			t.Errorf("%s: envelope not ok: %+v", c.name, resp)
		}
	}
}

func TestControlErrIncludesLogTail(t *testing.T) {
	resp := &ControlResponse{
		Result: "err", Code: "wg_tools_missing", Msg: "wireguard-tools install failed", Stage: "wg_setup",
		Logs: []string{"one", "two", "three", "apt install failed: E: Unable to locate package wireguard-tools"},
	}
	msg := controlErr(resp).Error()
	for _, want := range []string{"[wg_setup]", "wg_tools_missing", "Unable to locate package"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
}

// A run whose first responses are cut off mid-output (connection died) must be
// retried and succeed, not surface as "unexpected response".
func TestRunControlRetriesTruncatedResponses(t *testing.T) {
	shrinkTimings(t)
	var calls atomic.Int32
	srv := newFakeSSHServer(t, func(string, []byte) (string, int) {
		switch calls.Add(1) {
		case 1:
			return "", 0 // nothing at all
		case 2:
			return `{"proto":2,"result":"ok","da`, 0 // cut off
		default:
			return "sudo: unable to resolve host x\n" + okEnvelope + "\n", 0
		}
	})
	resp, _, err := runControl(srv.sshConfig(), []string{"probe"})
	if err != nil || !resp.IsOK() {
		t.Fatalf("runControl = %+v, %v; want success on the 3rd attempt", resp, err)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("server saw %d runs, want 3", n)
	}
}

func TestRunControlGivesUpWithClearMessage(t *testing.T) {
	shrinkTimings(t)
	srv := newFakeSSHServer(t, func(string, []byte) (string, int) { return "", 0 })
	_, _, err := runControl(srv.sshConfig(), []string{"logs", "--tail=200"})
	if err == nil || !strings.Contains(err.Error(), "не вернул ответа") || !strings.Contains(err.Error(), "logs --tail=200") {
		t.Fatalf("err = %v, want the empty-response message naming the command", err)
	}
}

func TestParsePoll(t *testing.T) {
	running := "FT_STATE running\nFT_PROGRESS\n[install]\nresolving latest version\nFT_END\n"
	st, err := parsePoll(running)
	if err != nil || st.State != "running" || st.ProgressCount != 2 || len(st.Progress) != 2 {
		t.Fatalf("running: %+v, %v", st, err)
	}

	done := "FT_STATE done 0\nFT_PROGRESS\n\nlast line\nFT_END\nFT_OUT\n" + okEnvelope + "\n\nFT_END\nFT_ERR\n\nFT_END\n"
	st, err = parsePoll(done)
	if err != nil || st.State != "done" || st.ExitCode != 0 || st.Out != okEnvelope {
		t.Fatalf("done: %+v, %v", st, err)
	}
	if st.ProgressCount != 2 || len(st.Progress) != 1 {
		t.Fatalf("progress accounting: count=%d shown=%d, want the blank line counted (offset) but not shown", st.ProgressCount, len(st.Progress))
	}

	if st, err := parsePoll("FT_STATE nojob\n"); err != nil || st.State != "nojob" {
		t.Fatalf("nojob: %+v, %v", st, err)
	}

	// A finished job whose answer was cut off before its result must be re-polled.
	if _, err := parsePoll("FT_STATE done 0\nFT_PROGRESS\nFT_END\nFT_OUT\n{\"proto\""); err == nil {
		t.Fatal("truncated done answer accepted as complete")
	}
	if _, err := parsePoll("garbage"); err == nil {
		t.Fatal("garbage accepted")
	}
}
