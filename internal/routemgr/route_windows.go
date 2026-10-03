//go:build windows

package routemgr

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// discoverGateway picks the default gateway Windows itself would actually
// use: the 0.0.0.0/0 route with the lowest metric. Tools like Radmin VPN or
// Hamachi install their own (high-metric, low-priority) default route via a
// virtual LAN-emulation adapter that can't reach the real internet; such a
// route often appears before the real one in "route print" output, so a
// naive first-match scan picks it and every exclusion route added through it
// becomes a dead end (worse than no exclusion route, since it's a more
// specific /32 that overrides otherwise-correct default routing).
func discoverGateway() (string, error) {
	cmd := exec.Command("cmd", "/c", "route", "print", "0.0.0.0") //nolint:gosec,noctx
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to run route print: %w", err)
	}

	bestGateway := ""
	bestMetric := 0
	inActiveRoutes := false

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "Active Routes"):
			inActiveRoutes = true
			continue
		case strings.HasPrefix(line, "Persistent Routes"):
			inActiveRoutes = false
			continue
		case line == "" || strings.HasPrefix(line, "=") || strings.HasPrefix(line, "Network"):
			continue
		}
		if !inActiveRoutes {
			continue
		}

		// Network Destination, Netmask, Gateway, Interface, Metric.
		fields := strings.Fields(line)
		if len(fields) != 5 || fields[0] != "0.0.0.0" || fields[1] != "0.0.0.0" {
			continue
		}
		metric, err := strconv.Atoi(fields[4])
		if err != nil {
			continue
		}
		if bestGateway == "" || metric < bestMetric {
			bestGateway = fields[2]
			bestMetric = metric
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error parsing route print output: %w", err)
	}
	if bestGateway == "" {
		return "", fmt.Errorf("default gateway not found")
	}

	return bestGateway, nil
}

func addRoute(ip, gateway string) error {
	cmd := exec.Command("cmd", "/c", "route", "add", ip, "mask", "255.255.255.255", gateway) //nolint:gosec,noctx
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add route for %s via %s: %w", ip, gateway, err)
	}
	return nil
}

func delRoute(ip string) error {
	cmd := exec.Command("cmd", "/c", "route", "delete", ip) //nolint:gosec,noctx
	_ = cmd.Run()
	return nil
}
