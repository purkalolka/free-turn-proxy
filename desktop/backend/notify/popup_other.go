//go:build !windows

package notify

func showCustomCaptchaPopup(captchaURL string) error {
	return errUnsupported
}
