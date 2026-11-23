// Package dlnanet discovers and controls UPnP MediaRenderers on a selected LAN.
package dlnanet

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"simplewebplayer/internal/casting"
)

var (
	errDiscovery = errors.New("dlna: local discovery unavailable")
	errTarget    = errors.New("dlna: renderer not confirmed on this LAN")
	errMedia     = errors.New("dlna: invalid local media")
	errReceiver  = errors.New("dlna: invalid or unavailable receiver response")
	errAction    = errors.New("dlna: receiver rejected the transport action")
	errPlayback  = errors.New("dlna: requested media not confirmed playing")
)

// checkedURL resolves references, then checks the *result*. Nothing in the
// authority may require DNS. The canonical explicit port is also the dial key.
func checkedURL(raw string, base *url.URL, peer netip.Addr) (*url.URL, netip.AddrPort, bool) {
	var zero netip.AddrPort
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\\# \t\r\n") {
		return nil, zero, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil {
		return nil, zero, false
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if !peer.Is4() || u.Scheme != "http" || u.Hostname() != peer.String() {
		return nil, zero, false
	}
	port := uint64(80)
	if u.Host != peer.String() {
		p := u.Port()
		port, err = strconv.ParseUint(p, 10, 16)
		if err != nil || port == 0 || p != strconv.FormatUint(port, 10) || u.Host != peer.String()+":"+p {
			return nil, zero, false
		}
	}
	origin := netip.AddrPortFrom(peer, uint16(port))
	u.Host = origin.String()
	if base != nil && base.Host != u.Host {
		return nil, zero, false
	}
	return u, origin, true
}

// Both the HTTP layer and the socket layer enforce the observed numeric origin.
// Never inherit ProxyFromEnvironment, follow redirects, or use the URL for DNS.
func clientFor(l casting.LAN, origin netip.AddrPort) *http.Client {
	fixed := origin.String()
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: l.Addr.AsSlice()}, Timeout: 2 * time.Second}
	return &http.Client{
		Timeout:       3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DisableCompression: true, DisableKeepAlives: true, MaxResponseHeaderBytes: 16 << 10,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if network != "tcp" || addr != fixed {
					return nil, errTarget
				}
				return d.DialContext(ctx, "tcp4", fixed)
			},
		},
	}
}

// A caller receives only bounded bytes or a generic error, never a *url.Error
// or a device-controlled fault string (both can include a bearer media URL).
func request(ctx context.Context, cl *http.Client, req *http.Request, limit int64) ([]byte, error) {
	resp, err := cl.Do(req.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errReceiver
	}
	defer resp.Body.Close()
	// UPnP returns a SOAP Fault as HTTP 500. Allow only its bounded body
	// through with a non-nil error so the SOAP layer can recognize fault 701.
	fault := req.Method == http.MethodPost && resp.StatusCode == http.StatusInternalServerError
	if resp.StatusCode != http.StatusOK && !fault {
		return nil, errAction
	}
	if resp.ContentLength > limit {
		return nil, errReceiver
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errReceiver
	}
	if fault {
		return b, errAction
	}
	return b, nil
}

// Cap XML nesting and token count before decoding typed structs. Reject DTDs,
// concatenated documents and non-whitespace content outside the document.
func validXML(b []byte) bool {
	d := xml.NewDecoder(bytes.NewReader(b))
	depth, roots := 0, 0
	for n := 0; n < 24000; n++ {
		tok, err := d.Token()
		if err != nil {
			return err == io.EOF && roots == 1 && depth == 0
		}
		switch v := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
			}
			depth++
			if roots > 1 || depth > 48 {
				return false
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			return false
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(v)) != 0 {
				return false
			}
		}
	}
	return false
}
