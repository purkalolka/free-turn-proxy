package udprelay

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
)

// Watchdog defaults for a single relay stream. A stream that still sends but never
// receives while its neighbours do is a dead allocation: the socket stays open and the
// read never errors, so only per-stream counters can tell it apart from a healthy one.
const (
	streamWatchInterval = 5 * time.Second
	streamSilence       = 20 * time.Second
	streamMinTxBytes    = 4096
)

// StreamInfo is a snapshot of one relay stream, in a shape that is safe for gomobile.
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

// Tracker keeps per-stream liveness so a single dead allocation can be recycled without
// tearing down the whole relay (which costs credentials and twelve handshakes).
type Tracker struct {
	mu      sync.Mutex
	streams map[int]*streamState
	now     func() time.Time
}

type streamState struct {
	up      bool
	since   time.Time
	rx      int64
	tx      int64
	lastRx  time.Time
	lastTx  time.Time
	allocs  int
	lastErr string
	cancel  func()
	// Bytes sent since the stream last received anything.
	txSinceRx int64
}

func NewTracker() *Tracker {
	return &Tracker{streams: make(map[int]*streamState), now: time.Now}
}

// activeTracker is the tracker of the relay attempt that is currently running. The mobile
// facade reads it, so the host can restart a single stream without a session-level API.
var activeTracker atomic.Pointer[Tracker]

// CurrentTracker returns the tracker of the running relay attempt, if any.
func CurrentTracker() *Tracker { return activeTracker.Load() }

func installTracker(t *Tracker) func() {
	activeTracker.Store(t)
	return func() { activeTracker.CompareAndSwap(t, nil) }
}

func (t *Tracker) state(id int) *streamState {
	s := t.streams[id]
	if s == nil {
		s = &streamState{}
		t.streams[id] = s
	}
	return s
}

// MarkUp records that an allocation for the stream is serving traffic. cancel tears down
// exactly this attempt when the stream has to be recycled.
func (t *Tracker) MarkUp(id int, cancel func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.state(id)
	now := t.now()
	s.up = true
	s.since = now
	s.lastRx = now
	s.lastTx = now
	s.allocs++
	s.cancel = cancel
	s.lastErr = ""
	s.txSinceRx = 0
}

// MarkDown records that the allocation is gone; the stream loops will re-dial it.
func (t *Tracker) MarkDown(id int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.state(id)
	s.up = false
	s.cancel = nil
	if err != nil {
		s.lastErr = err.Error()
	}
}

func (t *Tracker) AddRx(id int, n int) {
	if n <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.state(id)
	s.rx += int64(n)
	s.lastRx = t.now()
	s.txSinceRx = 0
}

func (t *Tracker) AddTx(id int, n int) {
	if n <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.state(id)
	s.tx += int64(n)
	s.lastTx = t.now()
	s.txSinceRx += int64(n)
}

// Restart tears down only the stream's current TURN attempt. Its DTLS loop reconnects on
// the next iteration, so the other streams and the WireGuard session are untouched.
func (t *Tracker) Restart(id int) bool {
	t.mu.Lock()
	s := t.streams[id]
	var cancel func()
	if s != nil && s.up {
		cancel = s.cancel
		s.cancel = nil
	}
	t.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// Stalled lists streams that sent bytes but received nothing for `silence`, while at
// least one neighbour is still receiving. Comparing with the neighbours keeps one-way
// traffic (a large upload, a quiet peer) from looking like a dead stream.
func (t *Tracker) Stalled(silence time.Duration, minTx int64) []int {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	neighbourAlive := false
	for _, s := range t.streams {
		if s.up && now.Sub(s.lastRx) < silence {
			neighbourAlive = true
			break
		}
	}
	if !neighbourAlive {
		// Nobody receives: the whole path is down, a per-stream restart would not help.
		return nil
	}
	var out []int
	for id, s := range t.streams {
		if !s.up {
			continue
		}
		if now.Sub(s.since) < silence || now.Sub(s.lastRx) < silence {
			continue // freshly re-dialed, or still receiving
		}
		if s.txSinceRx < minTx {
			continue
		}
		out = append(out, id)
	}
	return out
}

// RestartStalled recycles every stream the watchdog considers dead.
func (t *Tracker) RestartStalled() int {
	restarted := 0
	for _, id := range t.Stalled(streamSilence, streamMinTxBytes) {
		if t.Restart(id) {
			restarted++
		}
	}
	return restarted
}

func (t *Tracker) UpCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, s := range t.streams {
		if s.up {
			n++
		}
	}
	return n
}

func (t *Tracker) Snapshot() []*StreamInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	out := make([]*StreamInfo, 0, len(t.streams))
	for id, s := range t.streams {
		out = append(out, &StreamInfo{
			ID:           id,
			Up:           s.up,
			RxBytes:      s.rx,
			TxBytes:      s.tx,
			LastRxAgoSec: ageSec(now, s.lastRx),
			LastTxAgoSec: ageSec(now, s.lastTx),
			Allocs:       s.allocs,
			LastError:    s.lastErr,
		})
	}
	return out
}

func ageSec(now, at time.Time) int64 {
	if at.IsZero() {
		return -1
	}
	d := now.Sub(at)
	if d < 0 {
		return 0
	}
	return int64(d.Seconds())
}

// watchStreams recycles a stream that stopped receiving while its neighbours still do.
// A healthy stream is never touched, so a partial failure costs one allocation instead of
// the whole relay and twelve fresh handshakes.
func watchStreams(ctx context.Context, t *Tracker, logger logx.Logger) {
	ticker := time.NewTicker(streamWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, id := range t.Stalled(streamSilence, streamMinTxBytes) {
				logger.Warnf("[STREAM %d] silent for %s while others receive - recycling allocation", id, streamSilence)
				t.Restart(id)
			}
		}
	}
}
