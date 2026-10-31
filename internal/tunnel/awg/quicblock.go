package awg

import (
	"encoding/binary"
	"sync/atomic"

	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
)

// BlockQUIC включает фильтр исходящего UDP/443 (QUIC/HTTP3) на уровне tun.
// Через релей QUIC страдает от потерь и долго откатывается на TCP, поэтому
// пакеты отбрасываются, а источнику сразу возвращается ICMP port unreachable -
// приложение переключается на TCP без ожидания таймаута. Действует на туннели,
// поднятые после установки флага.
var BlockQUIC atomic.Bool

// writeOffset - запас перед пакетом для служебного заголовка tun (utun, 4 байта).
const writeOffset = 16

type quicFilter struct {
	tun.Device
}

func (f *quicFilter) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	n, err := f.Device.Read(bufs, sizes, offset)
	if err != nil || n == 0 {
		return n, err
	}
	for i := 0; i < n; {
		pkt := bufs[i][offset : offset+sizes[i]]
		reply := quicReject(pkt)
		if reply == nil {
			i++
			continue
		}
		out := make([]byte, writeOffset+len(reply))
		copy(out[writeOffset:], reply)
		_, _ = f.Device.Write([][]byte{out}, writeOffset)

		n--
		bufs[i], bufs[n] = bufs[n], bufs[i]
		sizes[i], sizes[n] = sizes[n], sizes[i]
	}
	return n, nil
}

// quicReject возвращает ICMP port unreachable для IPv4 UDP-пакета на порт 443
// (nil для всего остального).
func quicReject(pkt []byte) []byte {
	if len(pkt) < 28 || pkt[0]>>4 != 4 {
		return nil
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 || len(pkt) < ihl+8 || pkt[9] != 17 {
		return nil
	}
	if binary.BigEndian.Uint16(pkt[6:8])&0x1fff != 0 { // не первый фрагмент
		return nil
	}
	if binary.BigEndian.Uint16(pkt[ihl+2:ihl+4]) != 443 {
		return nil
	}

	quoted := pkt[:ihl+8]
	icmpLen := 8 + len(quoted)
	out := make([]byte, 20+icmpLen)
	out[0] = 0x45
	out[1] = 0xc0
	binary.BigEndian.PutUint16(out[2:4], uint16(len(out)))
	out[8] = 64
	out[9] = 1
	copy(out[12:16], pkt[16:20]) // src = исходный dst
	copy(out[16:20], pkt[12:16]) // dst = исходный src
	binary.BigEndian.PutUint16(out[10:12], checksum(out[:20]))

	icmp := out[20:]
	icmp[0] = 3
	icmp[1] = 3
	copy(icmp[8:], quoted)
	binary.BigEndian.PutUint16(icmp[2:4], checksum(icmp))
	return out
}

func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}
