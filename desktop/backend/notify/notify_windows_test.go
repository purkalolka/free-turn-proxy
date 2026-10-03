//go:build windows

package notify

import (
	"testing"
	"time"
)

func TestShowCaptchaNotificationThrottling(t *testing.T) {
	captchaMu.Lock()
	lastCaptchaTime = time.Time{}
	captchaMu.Unlock()

	called := 0
	onOpen := func() {
		called++
	}

	_ = ShowCaptchaNotification("https://example.com/captcha", onOpen)

	// Immediate next call should be throttled
	err := ShowCaptchaNotification("https://example.com/captcha", onOpen)
	if err != nil {
		t.Fatalf("expected nil error on throttled call, got: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if called > 1 {
		t.Fatalf("expected onOpen to be called at most once due to throttling, called %d times", called)
	}
}

func TestShowCustomCaptchaPopup(t *testing.T) {
	err := showCustomCaptchaPopup("https://example.com/captcha")
	if err != nil {
		t.Fatalf("showCustomCaptchaPopup failed: %v", err)
	}

	popupMu.Lock()
	hwnd := activePopupHwnd
	popupMu.Unlock()

	if hwnd == 0 {
		t.Fatal("expected activePopupHwnd to be non-zero")
	}

	// Calling it a second time should close previous and spawn new popup without issues
	err = showCustomCaptchaPopup("https://example.com/captcha2")
	if err != nil {
		t.Fatalf("second showCustomCaptchaPopup failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	popupMu.Lock()
	hwnd2 := activePopupHwnd
	popupMu.Unlock()

	if hwnd2 == 0 {
		t.Fatal("expected second popup to have activePopupHwnd")
	}

	// Close popup cleanly
	pPostMessageW.Call(uintptr(hwnd2), wmClose, 0, 0)
}
