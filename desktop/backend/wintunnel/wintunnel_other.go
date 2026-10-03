//go:build !windows

package wintunnel

import "errors"

var errUnsupported = errors.New("wintunnel: embedded VPN is only implemented on Windows")

func Available() bool { return false }

func Connect(configJSON, wgConfigText string, mtu int, exclusionIPs ...string) error { return errUnsupported }

func Disconnect() {}
