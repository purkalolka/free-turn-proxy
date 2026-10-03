package replay

import (
	"sync"
	"testing"
)

func TestFilterSequential(t *testing.T) {
	f := NewFilter()
	for i := uint64(1); i <= 200; i++ {
		if !f.Check(i) {
			t.Fatalf("seq %d should be allowed", i)
		}
		f.Accept(i)
		// Immediate duplicate must be rejected
		if f.Check(i) {
			t.Fatalf("duplicate seq %d should be rejected", i)
		}
	}
}

func TestFilterOutOfOrderWithinWindow(t *testing.T) {
	f := NewFilter()
	f.Accept(10)

	// Older packet within window (seq 8, diff=2)
	if !f.Check(8) {
		t.Fatal("seq 8 within window should be allowed")
	}
	f.Accept(8)

	// Duplicate of seq 8 must now be rejected
	if f.Check(8) {
		t.Fatal("duplicate seq 8 should be rejected")
	}

	// seq 9 within window
	if !f.Check(9) {
		t.Fatal("seq 9 within window should be allowed")
	}
	f.Accept(9)

	// Duplicate of seq 9 must now be rejected
	if f.Check(9) {
		t.Fatal("duplicate seq 9 should be rejected")
	}
}

func TestFilterPacketTooOld(t *testing.T) {
	f := NewFilter()
	f.Accept(200)

	// diff = 200 - 72 = 128 >= WindowSize -> too old
	if f.Check(72) {
		t.Fatal("seq 72 should be rejected as too old (diff=128)")
	}
	if f.Check(50) {
		t.Fatal("seq 50 should be rejected as too old")
	}

	// diff = 200 - 73 = 127 < WindowSize -> within window
	if !f.Check(73) {
		t.Fatal("seq 73 should be allowed (diff=127)")
	}
	f.Accept(73)
	if f.Check(73) {
		t.Fatal("seq 73 duplicate should be rejected")
	}
}

func TestFilterLargeJumpForward(t *testing.T) {
	f := NewFilter()
	f.Accept(10)

	// Jump by 300 packets
	if !f.Check(310) {
		t.Fatal("seq 310 should be allowed")
	}
	f.Accept(310)

	// Duplicate of 310
	if f.Check(310) {
		t.Fatal("duplicate 310 should be rejected")
	}

	// Packets from the old window (e.g. 10..181) must now be too old
	if f.Check(10) {
		t.Fatal("old seq 10 should be rejected")
	}
	if f.Check(182) {
		t.Fatal("seq 182 (diff 128) should be rejected")
	}

	// Packet at boundary (310 - 127 = 183) is within window
	if !f.Check(183) {
		t.Fatal("seq 183 (diff 127) should be allowed")
	}
	f.Accept(183)
	if f.Check(183) {
		t.Fatal("duplicate seq 183 should be rejected")
	}
}

func TestFilterConcurrent(t *testing.T) {
	f := NewFilter()
	var wg sync.WaitGroup

	for i := uint64(0); i < 100; i++ {
		wg.Add(1)
		go func(seq uint64) {
			defer wg.Done()
			if f.Check(seq) {
				f.Accept(seq)
			}
		}(i)
	}
	wg.Wait()
}
