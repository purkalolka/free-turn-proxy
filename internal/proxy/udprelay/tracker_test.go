package udprelay

import (
	"testing"
	"time"
)

func testTracker(clock *time.Time) *Tracker {
	t := NewTracker()
	t.now = func() time.Time { return *clock }
	return t
}

func TestStalledOnlyWhenNeighbourReceives(t *testing.T) {
	clock := time.Unix(1000, 0)
	tr := testTracker(&clock)
	cancelled := map[int]int{}
	tr.MarkUp(1, func() { cancelled[1]++ })
	tr.MarkUp(2, func() { cancelled[2]++ })
	tr.MarkUp(3, func() { cancelled[3]++ })

	clock = clock.Add(30 * time.Second)
	tr.AddTx(1, 9000)
	tr.AddTx(2, 9000)
	tr.AddRx(2, 5000)

	stalled := tr.Stalled(20*time.Second, 4096)
	if len(stalled) != 1 || stalled[0] != 1 {
		t.Fatalf("stalled = %v, want [1]", stalled)
	}
	if n := tr.RestartStalled(); n != 1 {
		t.Fatalf("RestartStalled = %d, want 1", n)
	}
	if cancelled[1] != 1 || cancelled[2] != 0 || cancelled[3] != 0 {
		t.Fatalf("cancelled = %v, want only stream 1", cancelled)
	}
}

func TestNoStallWhenNobodyReceives(t *testing.T) {
	clock := time.Unix(1000, 0)
	tr := testTracker(&clock)
	tr.MarkUp(1, func() {})
	tr.MarkUp(2, func() {})

	clock = clock.Add(30 * time.Second)
	tr.AddTx(1, 9000)
	tr.AddTx(2, 9000)

	if stalled := tr.Stalled(20*time.Second, 4096); len(stalled) != 0 {
		t.Fatalf("a fully black-holed relay must be left to the session, got %v", stalled)
	}
}

func TestFreshStreamHasGrace(t *testing.T) {
	clock := time.Unix(1000, 0)
	tr := testTracker(&clock)
	tr.MarkUp(1, func() {})
	tr.MarkUp(2, func() {})

	clock = clock.Add(5 * time.Second)
	tr.AddTx(1, 9000)
	tr.AddRx(2, 5000)

	if stalled := tr.Stalled(20*time.Second, 4096); len(stalled) != 0 {
		t.Fatalf("freshly re-dialed stream flagged: %v", stalled)
	}
}

func TestRestartAfterMarkDownIsNoop(t *testing.T) {
	clock := time.Unix(1000, 0)
	tr := testTracker(&clock)
	calls := 0
	tr.MarkUp(1, func() { calls++ })
	if !tr.Restart(1) {
		t.Fatal("Restart on a live stream = false")
	}
	tr.MarkDown(1, nil)
	if tr.Restart(1) {
		t.Fatal("Restart after MarkDown = true")
	}
	if calls != 1 {
		t.Fatalf("cancel calls = %d, want 1", calls)
	}
	if tr.UpCount() != 0 {
		t.Fatalf("UpCount = %d, want 0", tr.UpCount())
	}
}

func TestSnapshotCounters(t *testing.T) {
	clock := time.Unix(1000, 0)
	tr := testTracker(&clock)
	tr.MarkUp(7, func() {})
	tr.AddRx(7, 1000)
	tr.AddTx(7, 2000)

	clock = clock.Add(12 * time.Second)
	snapshot := tr.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("snapshot size = %d, want 1", len(snapshot))
	}
	s := snapshot[0]
	if s.ID != 7 || !s.Up || s.RxBytes != 1000 || s.TxBytes != 2000 {
		t.Fatalf("unexpected snapshot: %+v", s)
	}
	if s.LastRxAgoSec < 11 || s.LastRxAgoSec > 12 {
		t.Fatalf("LastRxAgoSec = %d, want ~12", s.LastRxAgoSec)
	}
	if s.Allocs != 1 {
		t.Fatalf("Allocs = %d, want 1", s.Allocs)
	}
}
