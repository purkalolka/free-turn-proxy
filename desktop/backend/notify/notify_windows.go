//go:build windows

package notify

import (
	"strings"
	"sync"
	"time"

	"git.sr.ht/~jackmordaunt/go-toast/v2"
)

const (
	appID          = "FreeTurn Desktop"
	captchaTitle   = "FreeTurn: требуется решение капчи VK"
	captchaMessage = "VK запросил проверку капчи для подключения. Нажмите, чтобы открыть."
	throttleWindow = 5 * time.Second
)

var (
	captchaMu       sync.Mutex
	lastCaptchaTime time.Time
)

// ShowCaptchaNotification sends a notification informing the user
// that VK captcha verification is required. Repeated calls within 5 seconds are throttled.
// If onOpen is provided, it is invoked when the notification is opened / triggered.
func ShowCaptchaNotification(captchaURL string, onOpen func()) error {
	captchaMu.Lock()
	now := time.Now()
	if now.Sub(lastCaptchaTime) < throttleWindow {
		captchaMu.Unlock()
		return nil
	}
	lastCaptchaTime = now
	captchaMu.Unlock()

	if onOpen != nil {
		go onOpen()
	}

	// Always trigger custom popup (guaranteed visibility even under Focus Assist / Do Not Disturb)
	_ = showCustomCaptchaPopup(captchaURL)

	return ShowNotification(captchaTitle, captchaMessage, captchaURL)
}

// ShowNotification sends a Windows toast notification with the given title, message and optional URL.
// If url is provided, an "Открыть" action button with protocol activation is attached,
// and clicking the notification body will also open the URL.
func ShowNotification(title, message, url string) error {
	trimmedURL := strings.TrimSpace(url)

	n := toast.Notification{
		AppID: titleAppID(),
		Title: title,
		Body:  message,
		Audio: toast.Default,
	}

	if trimmedURL != "" {
		n.ActivationType = toast.Protocol
		n.ActivationArguments = trimmedURL
		n.Actions = []toast.Action{
			{
				Type:      toast.Protocol,
				Content:   "Открыть",
				Arguments: trimmedURL,
			},
		}
	}

	return n.Push()
}

func titleAppID() string {
	return appID
}
