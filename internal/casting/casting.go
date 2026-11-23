// Package casting defines the application/transport boundary for local Google Cast.
package casting

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"path"
	"strconv"
	"strings"
)

const DevicePort = 8009

type LAN struct {
	Interface net.Interface
	Addr      netip.Addr
	Prefix    netip.Prefix
	Port      uint16
}

// Contains accepts only unicast peers on this private IPv4 subnet, excluding
// the server, subnet address and directed broadcast address.
func (l LAN) Contains(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.Is4() || !ip.IsPrivate() || !l.Prefix.Contains(ip) || ip == l.Addr {
		return false
	}
	bits := l.Prefix.Bits()
	if bits < 1 || bits > 30 {
		return false
	}
	network := l.Prefix.Masked().Addr().As4()
	b := ip.As4()
	if b == network {
		return false
	}
	for n := bits; n < 32; n++ {
		network[n/8] |= byte(1 << (7 - n%8))
	}
	return b != network
}

// Peer uses the socket address only; no forwarded address is ever trusted.
func Peer(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	ip, _ := netip.ParseAddr(host)
	return ip.Unmap()
}

// FromRequest resolves the interface serving a direct private IPv4 HTTP
// connection. A loopback or forwarded reverse-proxy request is not eligible.
func FromRequest(r *http.Request) (LAN, bool) {
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP", "CF-Connecting-IP", "CF-Ray"} {
		if len(r.Header.Values(name)) != 0 {
			return LAN{}, false
		}
	}
	peer := Peer(r)
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	if !ok || addr == nil || addr.Port < 1 || addr.Port > 65535 || r.TLS != nil || !peer.Is4() || !peer.IsPrivate() {
		return LAN{}, false
	}
	local, ok := netip.AddrFromSlice(addr.IP)
	local = local.Unmap()
	if !ok || !local.Is4() || !local.IsPrivate() {
		return LAN{}, false
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return LAN{}, false
	}
	for _, i := range interfaces {
		if i.Flags&(net.FlagUp|net.FlagMulticast) != net.FlagUp|net.FlagMulticast || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			p, e := netip.ParsePrefix(a.String())
			if e != nil || p.Addr().Unmap() != local || !p.Addr().Is4() {
				continue
			}
			l := LAN{Interface: i, Addr: local, Prefix: p.Masked(), Port: uint16(addr.Port)}
			if l.Contains(peer) {
				return l, true
			}
		}
	}
	return LAN{}, false
}

func (l LAN) HTTPAddress() string {
	return net.JoinHostPort(l.Addr.String(), strconv.Itoa(int(l.Port)))
}

type Device struct {
	Name  string
	Model string
	Addr  netip.Addr
	Port  int
}

type Media struct {
	URL          string
	Title        string
	ContentType  string
	StartSeconds float64
}

func ContentType(filename string) string {
	switch strings.ToLower(path.Ext(filename)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	default:
		return ""
	}
}

// Transport only contacts devices discovered on the provided LAN; Stop is
// conditional on mediaURL still being loaded so other apps are unaffected.
type Transport interface {
	Discover(context.Context, LAN) ([]Device, error)
	Load(context.Context, LAN, Device, Media) error
	Stop(context.Context, LAN, Device, string) error
}
