package dlnanet

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/ipv4"
	"simplewebplayer/internal/casting"
)

const (
	rendererURN   = "urn:schemas-upnp-org:device:MediaRenderer:"
	transportURN  = "urn:schemas-upnp-org:service:AVTransport:"
	maxPacket     = 8192
	maxCandidates = 24
	maxPerIP      = 4
)

var ssdp = &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}

type candidate struct {
	location *url.URL
	origin   netip.AddrPort
	until    time.Time
}

func validLAN(l casting.LAN) bool {
	return l.Interface.Index > 0 && l.Port != 0 && l.Addr.Is4() && l.Addr.IsPrivate() && l.Prefix.Addr().Is4() && l.Prefix.Bits() > 0 && l.Prefix.Bits() <= 30 && l.Prefix.Contains(l.Addr)
}

func version(s, prefix string) int {
	v, found := strings.CutPrefix(strings.TrimSpace(s), prefix)
	n, err := strconv.ParseUint(v, 10, 16)
	if !found || err != nil || n == 0 || strconv.FormatUint(n, 10) != v {
		return 0
	}
	return int(n)
}

// The IP comes only from recvfrom, never from USN, SERVER or LOCATION.
func fromReply(b []byte, source netip.Addr, l casting.LAN, now time.Time) (candidate, bool) {
	if len(b) == 0 || len(b) > maxPacket || !source.Is4() || !l.Contains(source) {
		return candidate{}, false
	}
	r, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(b)), nil)
	if err != nil {
		return candidate{}, false
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK || r.ProtoMajor != 1 || len(r.Header.Values("ST")) != 1 || len(r.Header.Values("Location")) != 1 {
		return candidate{}, false
	}
	st := r.Header.Get("ST")
	if version(st, rendererURN) == 0 && version(st, transportURN) == 0 {
		return candidate{}, false
	}
	u, origin, ok := checkedURL(r.Header.Get("Location"), nil, source)
	if !ok {
		return candidate{}, false
	}
	age := 90 * time.Second
	for _, v := range strings.Split(strings.ToLower(r.Header.Get("Cache-Control")), ",") {
		k, val, found := strings.Cut(v, "=")
		if !found || strings.TrimSpace(k) != "max-age" {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(val), 10, 32)
		if err != nil || n == 0 {
			return candidate{}, false
		}
		age = time.Duration(min(n, uint64(300))) * time.Second
		break
	}
	return candidate{u, origin, now.Add(age)}, true
}

func search(ctx context.Context, l casting.LAN) ([]candidate, error) {
	if !validLAN(l) {
		return nil, errDiscovery
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u, err := net.ListenUDP("udp4", &net.UDPAddr{IP: l.Addr.AsSlice()})
	if err != nil {
		return nil, errDiscovery
	}
	defer u.Close()
	stop := context.AfterFunc(ctx, func() { u.Close() })
	defer stop()
	p := ipv4.NewPacketConn(u)
	if p.SetMulticastTTL(1) != nil || p.SetMulticastInterface(&l.Interface) != nil || p.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true) != nil {
		return nil, errDiscovery
	}
	end := time.Now().Add(1600 * time.Millisecond)
	if d, ok := ctx.Deadline(); ok && d.Before(end) {
		end = d
	}
	if u.SetDeadline(end) != nil {
		return nil, errDiscovery
	}
	cm := &ipv4.ControlMessage{IfIndex: l.Interface.Index, Src: l.Addr.AsSlice()}
	for _, st := range []string{rendererURN + "1", transportURN + "1"} {
		msg := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 1\r\nST: " + st + "\r\n\r\n"
		if _, err := p.WriteTo([]byte(msg), cm, ssdp); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errDiscovery
		}
	}
	buf := make([]byte, maxPacket+1)
	seen := make(map[string]bool)
	perIP := make(map[netip.Addr]int)
	var found []candidate
	for reads := 0; reads < 192 && len(found) < maxCandidates && time.Now().Before(end); reads++ {
		n, meta, from, err := p.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if e, ok := err.(net.Error); ok && e.Timeout() {
				break
			}
			return nil, errDiscovery
		}
		a, ok := from.(*net.UDPAddr)
		if !ok || meta == nil || meta.IfIndex != l.Interface.Index || !meta.Dst.Equal(l.Addr.AsSlice()) {
			continue
		}
		peer := a.AddrPort().Addr().Unmap()
		if perIP[peer] >= maxPerIP {
			continue
		}
		c, ok := fromReply(buf[:n], peer, l, time.Now())
		if !ok || seen[c.location.String()] {
			continue
		}
		seen[c.location.String()] = true
		perIP[peer]++
		found = append(found, c)
	}
	return found, nil
}
