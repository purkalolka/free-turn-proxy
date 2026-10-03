// Package replay реализует скользящее окно защиты от повторных пакетов (Anti-Replay Window, RFC 6479 / RFC 4303).
package replay

import (
	"sync"
)

// WindowSize задаёт размер окна защиты от повторов в пакетах (128 пакетов).
const WindowSize = 128

// Filter реализует поточно-ориентированное скользящее окно для отсечения повторов и устаревших пакетов.
type Filter struct {
	mu      sync.Mutex
	lastSeq uint64
	bitmap  [2]uint64 // 128 бит (2 x 64)
	started bool
}

// NewFilter создаёт новый экземпляр фильтра повторов.
func NewFilter() *Filter {
	return &Filter{}
}

// Check проверяет, может ли пакет с номером seq быть принят (не дубликат и не устарел).
// Вызывается ДО расшифровки AEAD для быстрой защиты от DoS-атак повторными пакетами.
func (f *Filter) Check(seq uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.started {
		return true
	}

	if seq > f.lastSeq {
		return true
	}

	diff := f.lastSeq - seq
	if diff >= WindowSize {
		// Пакет слишком старый (выпал за пределы скользящего окна)
		return false
	}

	word := diff / 64
	bit := diff % 64
	// Если бит установлен - пакет уже был принят ранее
	return (f.bitmap[word] & (uint64(1) << bit)) == 0
}

// Accept регистрирует seq в окне ПОСЛЕ успешной расшифровки и проверки аутентичности AEAD.
func (f *Filter) Accept(seq uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.started {
		f.started = true
		f.lastSeq = seq
		f.bitmap[0] = 1
		f.bitmap[1] = 0
		return
	}

	if seq > f.lastSeq {
		diff := seq - f.lastSeq
		if diff >= WindowSize {
			f.bitmap[0] = 1
			f.bitmap[1] = 0
		} else {
			f.shift(diff)
			f.bitmap[0] |= 1
		}
		f.lastSeq = seq
		return
	}

	diff := f.lastSeq - seq
	if diff < WindowSize {
		word := diff / 64
		bit := diff % 64
		f.bitmap[word] |= (uint64(1) << bit)
	}
}

// shift сдвигает 128-битную маску [2]uint64 влево на diff бит.
func (f *Filter) shift(diff uint64) {
	if diff >= 128 {
		f.bitmap[0] = 0
		f.bitmap[1] = 0
		return
	}
	if diff >= 64 {
		f.bitmap[1] = f.bitmap[0] << (diff - 64)
		f.bitmap[0] = 0
		return
	}
	f.bitmap[1] = (f.bitmap[1] << diff) | (f.bitmap[0] >> (64 - diff))
	f.bitmap[0] = f.bitmap[0] << diff
}
