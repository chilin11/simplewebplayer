package castnet

import (
	"context"
	"crypto/tls"
	"errors"
	"math"
	"net"
	"net/netip"
	"net/url"
	"sync"
	"time"

	"simplewebplayer/internal/casting"
)

type peerKey struct {
	iface       int
	local, peer netip.Addr
}

type Transport struct {
	mu       sync.Mutex
	observed map[peerKey]time.Time
}

func New() *Transport { return &Transport{observed: make(map[peerKey]time.Time)} }

var _ casting.Transport = (*Transport)(nil)

var (
	errTarget  = errors.New("cast: device not confirmed on this LAN")
	errMedia   = errors.New("cast: invalid local media")
	errChannel = errors.New("cast: receiver channel unavailable")
)

func (t *Transport) Discover(ctx context.Context, l casting.LAN) ([]casting.Device, error) {
	a, err := scanLAN(ctx, l)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.observed == nil {
		t.observed = make(map[peerKey]time.Time)
	}
	for k, until := range t.observed {
		if !until.After(now) || k.iface == l.Interface.Index && k.local == l.Addr {
			delete(t.observed, k)
		}
	}
	ds := make([]casting.Device, 0, len(a))
	for _, ad := range a {
		if ad.until.After(now) {
			d := ad.device
			ds = append(ds, d)
			t.observed[peerKey{l.Interface.Index, l.Addr, d.Addr}] = ad.until
		}
	}
	for len(t.observed) > 128 {
		var victim peerKey
		var minUntil time.Time
		for k, until := range t.observed {
			if minUntil.IsZero() || until.Before(minUntil) {
				victim = k
				minUntil = until
			}
		}
		delete(t.observed, victim)
	}
	return ds, nil
}

func (t *Transport) confirmed(l casting.LAN, d casting.Device) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.observed[peerKey{l.Interface.Index, l.Addr, d.Addr}].After(time.Now())
}

func localURL(l casting.LAN, raw string) bool {
	if l.Port == 0 || len(raw) == 0 || len(raw) > 8192 {
		return false
	}
	u, err := url.ParseRequestURI(raw)
	return err == nil && u.Scheme == "http" && u.User == nil && u.Host == l.HTTPAddress() && u.Fragment == "" && len(u.Path) > 0
}

func (t *Transport) withDevice(ctx context.Context, l casting.LAN, d casting.Device, op func(*session) error) error {
	if !validLAN(l) || !d.Addr.Is4() || !l.Contains(d.Addr) || d.Port != casting.DevicePort {
		return errTarget
	}
	ctx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	if !t.confirmed(l, d) {
		// A long-running media lease may outlive the DNS TTL. Re-discover
		// on the authorized interface rather than dialing a stale address.
		if _, err := t.Discover(ctx, l); err != nil {
			return err
		}
		if !t.confirmed(l, d) {
			return errTarget
		}
	}
	// Numeric tcp4 address only, from our mDNS provenance cache; bind to the
	// LAN that also serves the HTTP media. No proxy, DNS resolution or URL dial.
	dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: l.Addr.AsSlice()}, Timeout: 4 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp4", netip.AddrPortFrom(d.Addr, casting.DevicePort).String())
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errChannel
	}
	return runChannel(ctx, raw, op)
}

// Caller provides a deadline and transfers ownership of the raw connection.
func runChannel(ctx context.Context, raw net.Conn, op func(*session) error) error {
	defer raw.Close()
	// Cast receivers commonly use self-signed certificates. This local-only
	// TLS dial encrypts the hop; it does NOT authenticate Google hardware.
	// Never share this config with HTTP or any other transport.
	conn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	deadline, _ := ctx.Deadline()
	if raw.SetDeadline(deadline) != nil {
		return errChannel
	}
	done := context.AfterFunc(ctx, func() { raw.Close() })
	defer done()
	err := conn.HandshakeContext(ctx)
	if err == nil {
		err = op(newSession(conn))
	} else {
		err = errChannel
	}
	// No STOP, receiver shutdown or application CLOSE at detach.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (t *Transport) Load(ctx context.Context, l casting.LAN, d casting.Device, m casting.Media) error {
	if !localURL(l, m.URL) || (m.ContentType != "video/mp4" && m.ContentType != "video/webm") || len(m.Title) > 4096 || math.IsNaN(m.StartSeconds) || math.IsInf(m.StartSeconds, 0) || m.StartSeconds < 0 {
		return errMedia
	}
	return t.withDevice(ctx, l, d, func(s *session) error { return s.load(m) })
}

func (t *Transport) Stop(ctx context.Context, l casting.LAN, d casting.Device, rawURL string) error {
	if !localURL(l, rawURL) {
		return errMedia
	}
	return t.withDevice(ctx, l, d, func(s *session) error { return s.stop(rawURL) })
}
