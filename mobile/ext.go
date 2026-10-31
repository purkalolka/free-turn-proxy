package mobile

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/samosvalishe/free-turn-proxy/internal/tunnel/awg"
)

// TuneForExtension подгоняет Go-рантайм под жёсткий лимит памяти iOS Network
// Extension (~50 МБ): без мягкого лимита GC не успевает до того, как процесс
// упрётся в потолок, а у самого потолка крутится почти непрерывно.
//
// Заодно включает отсечение QUIC (UDP/443) в туннеле - см. awg.BlockQUIC.
func TuneForExtension() {
	awg.BlockQUIC.Store(true)
	debug.SetMemoryLimit(35 << 20)
}

// MemStats возвращает краткую строку с потреблением памяти рантайма для лога хоста.
func MemStats() string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	out := fmt.Sprintf("heap=%dMB sys=%dMB gc=%d goroutines=%d",
		m.HeapAlloc>>20, m.Sys>>20, m.NumGC, runtime.NumGoroutine())
	if st := GetState(); st != nil {
		out += fmt.Sprintf(" | relay streams=%d/%d rx=%dKB/s tx=%dKB/s rxTotal=%dKB txTotal=%dKB",
			st.Streams, st.Total, st.RxRate>>10, st.TxRate>>10, st.RxTotal>>10, st.TxTotal>>10)
	}
	if ts := TunnelStats(); ts != nil {
		out += fmt.Sprintf(" | wg rx=%dKB tx=%dKB hs=%ds", ts.RxBytes>>10, ts.TxBytes>>10, ts.HandshakeAgeSec)
	}
	return out
}
