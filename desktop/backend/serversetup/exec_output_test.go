package serversetup

import "testing"

// Regression: with stdout and stderr sharing one buffer, output was lost at random
// (an empty result with no error). Hammer exec and require every response intact.
func TestExecNeverLosesOutput(t *testing.T) {
	shrinkTimings(t)
	poolIdleTimeout = 1 << 40
	srv := newFakeSSHServer(t, func(string, []byte) (string, int) { return "the-json-envelope", 0 })
	cfg := srv.sshConfig()
	for i := 0; i < 500; i++ {
		res, err := exec(cfg, "x", "")
		if err != nil || res.output != "the-json-envelope" {
			t.Fatalf("iteration %d: output=%q err=%v (output lost)", i, res.output, err)
		}
	}
}
