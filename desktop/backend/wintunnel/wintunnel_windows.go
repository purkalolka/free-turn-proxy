//go:build windows

// Package wintunnel is the desktop counterpart to the Android app's embedded
// VPN mode: it creates a real Windows network adapter (wintun) and points
// the free-turn-proxy engine's userspace WireGuard/AmneziaWG tunnel at it
// in-process, so the user doesn't need a separate WireGuard/AmneziaWG client
// pointed at the relay's local port. Interface configuration (IP/routes/DNS)
// follows the same steps the real wireguard-windows app uses (see its
// tunnel/addressconfig.go) via the same winipcfg library.
package wintunnel

import (
	_ "embed"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	amtun "github.com/amnezia-vpn/amneziawg-go/v3/tun"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/routemgr"
	"github.com/samosvalishe/free-turn-proxy/mobile"
)

// wintunDLL is the official Wintun driver library (WireGuard LLC,
// https://www.wintun.net/, MIT-compatible for redistribution with
// WireGuard-protocol software - the same binary wireguard-windows and
// AmneziaWG's own Windows client ship). The amneziawg-go tun package loads
// it by LoadLibraryEx'ing "wintun.dll" from the exe's own directory or
// System32, so it has to land on disk next to the exe before CreateTUN runs;
// embedding it keeps the built exe self-contained instead of needing a
// separate file alongside it that's easy to forget to ship.
//
//go:embed assets/wintun.dll
var wintunDLL []byte

const adapterName = "FreeTurn"

// DefaultMTU matches the server-control script's WG_MTU: the tunnel rides
// over TURN + DTLS + obfuscation overhead, so the usual 1420 doesn't fit.
const DefaultMTU = 1280

// connectTimeout bounds how long Connect waits for the relay session to
// actually establish (VK captcha solving + TURN allocation observed up to
// ~15s in practice) before giving up and tearing the adapter back down.
const connectTimeout = 45 * time.Second

// connState is everything Disconnect needs to tear down cleanly.
type connState struct {
	luid   winipcfg.LUID
	routes *routemgr.Manager // nil if gateway discovery failed - connect still proceeds, just without exclusion routes
}

var mu sync.Mutex
var active *connState

// IP_UNICAST_IF / IPV6_UNICAST_IF: not defined in golang.org/x/sys/windows.
// Same numeric option value, different level (IPPROTO_IP vs IPPROTO_IPV6) -
// see ws2ipdef.h.
const (
	ipUnicastIF   = 31
	ipv6UnicastIF = 31
)

// winProtector is the desktop equivalent of Android's VpnService.protect(fd):
// it pins a socket to the machine's real internet-facing interface via
// IP_UNICAST_IF/IPV6_UNICAST_IF, which overrides the routing table for that
// socket's own traffic - so it keeps working no matter what the tunnel's own
// default route says. This is what mobile.SetProtect/netctl already plumb
// into every socket the engine opens for itself (TURN dials, DNS queries,
// the VK HTTP client); Android/iOS supply their OS-level protect callback,
// this supplies the Windows one. It replaces trying to enumerate every IP
// the engine might ever need to reach (TURN servers change per session, VK's
// web/API IPs aren't fixed, DNS resolvers vary) with excluding the process
// itself, which is what actually has no OS-level VPN exemption on Windows.
type winProtector struct{ ifIndex uint32 }

// Protect implements mobile.Protector. It doesn't know the socket's address
// family up front, so it tries both options - the mismatched one just fails
// harmlessly (wrong protocol level for that socket).
func (p *winProtector) Protect(fd int) bool {
	h := windows.Handle(fd)
	err4 := windows.SetsockoptInt(h, windows.IPPROTO_IP, ipUnicastIF, int(htonl(p.ifIndex)))
	err6 := windows.SetsockoptInt(h, windows.IPPROTO_IPV6, ipv6UnicastIF, int(p.ifIndex))
	return err4 == nil || err6 == nil
}

func htonl(v uint32) uint32 {
	return (v>>24)&0xff | (v>>8)&0xff00 | (v<<8)&0xff0000 | (v<<24)&0xff000000
}

// discoverInterfaceIndex asks Windows which interface it would itself pick
// to reach the internet right now - the same metric-aware decision the
// routing table makes for a real connection, via the official API instead of
// re-parsing "route print" text. Must be called before the tunnel's own
// routes exist, or it would just answer "the tunnel".
func discoverInterfaceIndex() (uint32, error) {
	sa := &windows.SockaddrInet4{Addr: [4]byte{1, 1, 1, 1}, Port: 0}
	var idx uint32
	if err := windows.GetBestInterfaceEx(sa, &idx); err != nil {
		return 0, fmt.Errorf("get best interface: %w", err)
	}
	return idx, nil
}

// Available reports whether this build can create an embedded tunnel.
func Available() bool { return true }

// ensureWintunDLL writes the embedded wintun.dll next to the running exe if
// it isn't already there (size match is enough - it's our own embedded copy,
// not something a user would hand-edit).
func ensureWintunDLL() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	dst := filepath.Join(filepath.Dir(exe), "wintun.dll")
	if info, err := os.Stat(dst); err == nil && info.Size() == int64(len(wintunDLL)) {
		return nil
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, wintunDLL, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("install %s: %w", dst, err)
	}
	return nil
}

// Connect creates the wintun adapter, gives it just its own address, starts
// the session against it, and only once the relay actually connects does it
// cut the machine's default route over to the tunnel. wgConfigText is a
// standard wg-quick style config - Endpoint is ignored, the tunnel runs over
// an in-memory pipe to the relay session instead of a real UDP socket. Only
// one connection at a time; call Disconnect first to switch.
//
// The two-phase address-then-routes sequencing matters: the relay session
// needs real network access (DNS, VK auth, TURN allocation) before it can
// connect at all, and none of that exists yet at the point Connect is
// called. Installing the tunnel's 0.0.0.0/0 route up front would capture
// that same bootstrap traffic into a tunnel that can't carry it yet - a
// deadlock (unlike Android/iOS, Windows has no per-process VPN exemption to
// save it from this). So phase 1 only assigns the interface's own address
// (irrelevant to other traffic's routing) and starts the session on the
// machine's normal route; phase 2, once connected, installs the real routes.
// If exclusionIPs are provided (e.g. VPS server IP for SSH management), static
// host routes (/32) are installed via the physical default gateway so that server
// control and SSH remain reachable while the VPN is active.
func Connect(configJSON, wgConfigText string, mtu int, exclusionIPs ...string) error {
	mu.Lock()
	alreadyActive := active != nil
	mu.Unlock()
	if alreadyActive {
		return fmt.Errorf("wintunnel: already connected")
	}
	// mu stays unlocked from here through waitConnected below - it can take
	// tens of seconds (captcha, TURN allocation), and Disconnect must be able
	// to interrupt it immediately (mobile.Stop() breaks the wait loop out of
	// its poll) rather than blocking behind this call until it finishes.
	if mtu <= 0 {
		mtu = DefaultMTU
	}
	if err := ensureWintunDLL(); err != nil {
		return fmt.Errorf("install wintun.dll: %w", err)
	}

	params, err := mobile.ParseTunnelConfig(wgConfigText, mtu)
	if err != nil {
		return fmt.Errorf("parse tunnel config: %w", err)
	}

	// Capture the machine's current (pre-tunnel) default gateway before we
	// touch the route table at all, for exclusion routes covering ongoing
	// TURN reconnects/refreshes once phase 2 below makes the tunnel the
	// default route (the same bootstrap deadlock, just later: a wake/reauth
	// mid-session would otherwise hit it too).
	routes, rmErr := routemgr.New(logx.Nop())
	if rmErr != nil {
		routes = nil // proceed without exclusion routes rather than failing Connect outright
	}

	// Pin the engine's own sockets (TURN dials, DNS queries, VK's HTTP
	// client - anything going through netconn/dnsdial/turndial) to the same
	// real interface, for the whole lifetime of the connection, not just
	// known-at-the-time TURN IPs. This is the same mobile.SetProtect hook
	// Android feeds VpnService.protect(fd) through; exclusion routes above
	// are reactive (added only after a first successful connect to a given
	// IP) and only ever covered TURN, so DNS lookups and connections to
	// not-yet-seen IPs after phase 2's cutover still deadlocked. Cleared on
	// every return path below and in Disconnect.
	if idx, ifErr := discoverInterfaceIndex(); ifErr == nil {
		mobile.SetProtect(&winProtector{ifIndex: idx})
	}

	dev, err := amtun.CreateTUN(adapterName, params.MTU)
	if err != nil {
		mobile.SetProtect(nil)
		if routes != nil {
			routes.Close()
		}
		return fmt.Errorf("create wintun adapter: %w", err)
	}
	nativeTun, ok := dev.(*amtun.NativeTun)
	if !ok {
		dev.Close()
		mobile.SetProtect(nil)
		if routes != nil {
			routes.Close()
		}
		return fmt.Errorf("wintunnel: unexpected tun.Device implementation %T", dev)
	}
	luid := winipcfg.LUID(nativeTun.LUID())

	if err := configureAddress(luid, params); err != nil {
		flushInterface(luid)
		dev.Close()
		mobile.SetProtect(nil)
		if routes != nil {
			routes.Close()
		}
		return fmt.Errorf("configure adapter address: %w", err)
	}

	// StartTunnelDevice takes ownership of dev from here - on failure it
	// closes it itself, we only need to undo our own IP/route/DNS config.
	if err := mobile.StartTunnelDevice(configJSON, dev, routes.Callback()); err != nil {
		flushInterface(luid)
		mobile.SetProtect(nil)
		if routes != nil {
			routes.Close()
		}
		return err
	}

	if err := waitConnected(connectTimeout); err != nil {
		mobile.Stop()
		flushInterface(luid)
		mobile.SetProtect(nil)
		if routes != nil {
			routes.Close()
		}
		return err
	}

	if err := configureRoutesAndDNS(luid, params); err != nil {
		mobile.Stop()
		flushInterface(luid)
		mobile.SetProtect(nil)
		if routes != nil {
			routes.Close()
		}
		return fmt.Errorf("configure routes: %w", err)
	}

	if routes != nil {
		for _, ipStr := range exclusionIPs {
			routes.EnsureRoute(ipStr)
		}
	}

	mu.Lock()
	active = &connState{luid: luid, routes: routes}
	mu.Unlock()
	return nil
}

// Disconnect stops the session and removes the IP/route/DNS configuration
// this package applied (both the tunnel interface and any TURN exclusion
// routes). Safe to call when nothing is connected.
func Disconnect() {
	mu.Lock()
	st := active
	active = nil
	mu.Unlock()

	mobile.Stop()
	mobile.SetProtect(nil)
	if st != nil {
		flushInterface(st.luid)
		if st.routes != nil {
			st.routes.Close()
		}
	}
}

func flushInterface(luid winipcfg.LUID) {
	luid.FlushRoutes(windows.AF_UNSPEC)
	luid.FlushIPAddresses(windows.AF_UNSPEC)
	luid.FlushDNS(windows.AF_INET)
	luid.FlushDNS(windows.AF_INET6)
}

// waitConnected polls the session's state until it reaches "connected" (the
// relay is actually up and passing traffic), fails, or times out. Captcha
// solving is a normal intermediate state, not a failure, so it just keeps
// waiting through it.
func waitConnected(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		snap := mobile.GetState()
		switch snap.State {
		case mobile.StateConnected:
			return nil
		case mobile.StateError:
			if snap.ErrMsg != "" {
				return fmt.Errorf("сессия не подключилась: %s", snap.ErrMsg)
			}
			return fmt.Errorf("сессия завершилась с ошибкой")
		case mobile.StateIdle:
			return fmt.Errorf("сессия остановилась, не успев подключиться")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("не удалось подключиться за %s", timeout)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// configureAddress assigns the tunnel interface its own address only - safe
// to do before the session has connected, since (unlike routes) it has no
// effect on how the OS routes other traffic.
func configureAddress(luid winipcfg.LUID, params *mobile.TunnelParams) error {
	addresses, err := parsePrefixes(params.Addresses)
	if err != nil {
		return fmt.Errorf("addresses: %w", err)
	}
	if len(addresses) == 0 {
		return nil
	}
	if err := luid.SetIPAddresses(addresses); err != nil {
		return fmt.Errorf("set ip addresses: %w", err)
	}
	return nil
}

// configureRoutesAndDNS mirrors wireguard-windows's own tunnel/addressconfig.go
// (routes from AllowedIPs with an unspecified next-hop, MTU/DAD/router-
// discovery via the IP interface row, then DNS) minus its boot-time retry
// loop, which only matters when running as a service started before the
// network stack is up - not the case for an interactively launched app. Only
// call this once the session is confirmed connected (see Connect) - routes
// (especially a 0.0.0.0/0 one) make this interface the default route for
// every other app's traffic too, so switching it on too early starves the
// session of the bootstrap connectivity it needed to ever get here.
func configureRoutesAndDNS(luid winipcfg.LUID, params *mobile.TunnelParams) error {
	addresses, err := parsePrefixes(params.Addresses)
	if err != nil {
		return fmt.Errorf("addresses: %w", err)
	}
	allowedIPs, err := parsePrefixes(params.AllowedIPs)
	if err != nil {
		return fmt.Errorf("allowed ips: %w", err)
	}
	dnsServers, err := parseAddrs(params.DNS)
	if err != nil {
		return fmt.Errorf("dns: %w", err)
	}

	have4, have6 := false, false
	for _, a := range addresses {
		have4 = have4 || a.Addr().Is4()
		have6 = have6 || a.Addr().Is6()
	}

	routes := make([]*winipcfg.RouteData, 0, len(allowedIPs))
	route4, route6 := false, false
	for _, prefix := range allowedIPs {
		rd := &winipcfg.RouteData{Destination: prefix.Masked(), Metric: 0}
		if prefix.Addr().Is4() {
			rd.NextHop = netip.IPv4Unspecified()
			route4 = true
		} else {
			rd.NextHop = netip.IPv6Unspecified()
			route6 = true
		}
		routes = append(routes, rd)
	}
	if len(routes) > 0 {
		if err := luid.SetRoutes(routes); err != nil {
			return fmt.Errorf("set routes: %w", err)
		}
	}

	for _, family := range []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6} {
		if (family == windows.AF_INET && !(have4 || route4)) || (family == windows.AF_INET6 && !(have6 || route6)) {
			continue
		}
		ipif, err := luid.IPInterface(family)
		if err != nil {
			return fmt.Errorf("ip interface: %w", err)
		}
		ipif.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
		ipif.DadTransmits = 0
		ipif.ManagedAddressConfigurationSupported = false
		ipif.OtherStatefulConfigurationSupported = false
		ipif.NLMTU = uint32(params.MTU)
		ipif.UseAutomaticMetric = false
		ipif.Metric = 0
		if err := ipif.Set(); err != nil {
			return fmt.Errorf("set ip interface (family %d): %w", family, err)
		}
	}

	if len(dnsServers) > 0 {
		v4, v6 := splitByFamily(dnsServers)
		if len(v4) > 0 {
			if err := luid.SetDNS(windows.AF_INET, v4, nil); err != nil {
				return fmt.Errorf("set dns v4: %w", err)
			}
		}
		if len(v6) > 0 {
			if err := luid.SetDNS(windows.AF_INET6, v6, nil); err != nil {
				return fmt.Errorf("set dns v6: %w", err)
			}
		}
	}
	return nil
}

func splitByFamily(addrs []netip.Addr) (v4, v6 []netip.Addr) {
	for _, a := range addrs {
		if a.Is4() {
			v4 = append(v4, a)
		} else {
			v6 = append(v6, a)
		}
	}
	return v4, v6
}

func parsePrefixes(commaList string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range splitCSV(commaList) {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", s, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func parseAddrs(commaList string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, s := range splitCSV(commaList) {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", s, err)
		}
		out = append(out, a)
	}
	return out, nil
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
