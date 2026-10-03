//go:build windows

package tray

import (
	"testing"
	"time"
)

func TestTrayLifecycle(t *testing.T) {
	showCalled := false
	quitCalled := false

	tray, err := Start("FreeTurn Desktop Test", func() {
		showCalled = true
	}, func() {
		quitCalled = true
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if tray.hwnd == 0 {
		t.Fatalf("expected valid hwnd, got 0")
	}

	time.Sleep(100 * time.Millisecond)

	tray.Close()

	// Ensure multiple Close calls are safe
	tray.Close()

	_ = showCalled
	_ = quitCalled
}
