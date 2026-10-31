package mobile

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// TuneForExtension подгоняет Go-рантайм под жёсткий лимит памяти iOS Network
// Extension (~50 МБ): без мягкого лимита GC не успевает до того, как процесс
// упрётся в потолок, а у самого потолка крутится почти непрерывно.
func TuneForExtension() {
	debug.SetMemoryLimit(35 << 20)
	debug.SetGCPercent(50)
}

// MemStats возвращает краткую строку с потреблением памяти рантайма для лога хоста.
func MemStats() string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return fmt.Sprintf("heap=%dMB sys=%dMB gc=%d goroutines=%d",
		m.HeapAlloc>>20, m.Sys>>20, m.NumGC, runtime.NumGoroutine())
}
