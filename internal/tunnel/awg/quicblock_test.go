package awg

import (
	"encoding/binary"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
)

func udp4(dport uint16) []byte {
	p := make([]byte, 28+10)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	p[8] = 64
	p[9] = 17
	copy(p[12:16], []byte{10, 13, 13, 10})
	copy(p[16:20], []byte{1, 2, 3, 4})
	binary.BigEndian.PutUint16(p[20:22], 50000)
	binary.BigEndian.PutUint16(p[22:24], dport)
	return p
}

func TestQuicReject(t *testing.T) {
	if quicReject(udp4(53)) != nil {
		t.Error("udp/53 must pass")
	}
	tcp := udp4(443)
	tcp[9] = 6
	if quicReject(tcp) != nil {
		t.Error("tcp/443 must pass")
	}
	r := quicReject(udp4(443))
	if r == nil {
		t.Fatal("udp/443 must be rejected")
	}
	if r[9] != 1 || r[20] != 3 || r[21] != 3 {
		t.Errorf("not an ICMP port unreachable: %v", r[:24])
	}
	if string(r[12:16]) != string([]byte{1, 2, 3, 4}) || string(r[16:20]) != string([]byte{10, 13, 13, 10}) {
		t.Error("addresses not swapped")
	}
	if checksum(r[:20]) != 0 || checksum(r[20:]) != 0 {
		t.Error("bad checksum")
	}
}

type fakeTun struct {
	tun.Device
	in      [][]byte
	written [][]byte
}

func (f *fakeTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	for i, p := range f.in {
		copy(bufs[i][offset:], p)
		sizes[i] = len(p)
	}
	return len(f.in), nil
}

func (f *fakeTun) Write(bufs [][]byte, offset int) (int, error) {
	for _, b := range bufs {
		f.written = append(f.written, b[offset:])
	}
	return len(bufs), nil
}

func TestQuicFilterRead(t *testing.T) {
	ft := &fakeTun{in: [][]byte{udp4(443), udp4(53), udp4(443)}}
	f := &quicFilter{Device: ft}
	bufs := [][]byte{make([]byte, 100), make([]byte, 100), make([]byte, 100)}
	sizes := make([]int, 3)
	n, err := f.Read(bufs, sizes, 4)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if binary.BigEndian.Uint16(bufs[0][4+22:4+24]) != 53 {
		t.Error("wrong packet kept")
	}
	if len(ft.written) != 2 {
		t.Errorf("icmp replies = %d, want 2", len(ft.written))
	}
}
