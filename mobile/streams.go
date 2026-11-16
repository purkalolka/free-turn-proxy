package mobile

import "github.com/samosvalishe/free-turn-proxy/internal/proxy/udprelay"

// StreamInfo is one relay stream as seen by the host. Times are ages in seconds because
// gomobile cannot bind time.Time.
type StreamInfo struct {
	ID           int
	Up           bool
	RxBytes      int64
	TxBytes      int64
	LastRxAgoSec int64
	LastTxAgoSec int64
	Allocs       int
	LastError    string
}

// StreamsDetail returns a snapshot of the relay streams of the running session.
func StreamsDetail() []*StreamInfo {
	tracker := udprelay.CurrentTracker()
	if tracker == nil {
		return nil
	}
	snapshot := tracker.Snapshot()
	out := make([]*StreamInfo, 0, len(snapshot))
	for _, s := range snapshot {
		out = append(out, &StreamInfo{
			ID:           s.ID,
			Up:           s.Up,
			RxBytes:      s.RxBytes,
			TxBytes:      s.TxBytes,
			LastRxAgoSec: s.LastRxAgoSec,
			LastTxAgoSec: s.LastTxAgoSec,
			Allocs:       s.Allocs,
			LastError:    s.LastError,
		})
	}
	return out
}

// RestartStream recycles one stream: only its TURN allocation and DTLS session are
// re-created, the rest of the relay and the WireGuard tunnel keep running.
func RestartStream(id int) bool {
	tracker := udprelay.CurrentTracker()
	if tracker == nil {
		return false
	}
	return tracker.Restart(id)
}

// EnsureStreams recycles the streams that stopped receiving while their neighbours still
// do, and returns how many it restarted. It is the cheap alternative to Reconnect() and
// deliberately does not touch a relay that has not come up yet.
func EnsureStreams() int {
	tracker := udprelay.CurrentTracker()
	if tracker == nil {
		return 0
	}
	return tracker.RestartStalled()
}
