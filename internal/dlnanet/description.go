package dlnanet

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"simplewebplayer/internal/casting"
)

type service struct {
	Type    string `xml:"serviceType"`
	Control string `xml:"controlURL"`
}
type deviceXML struct {
	Type     string      `xml:"deviceType"`
	Name     string      `xml:"friendlyName"`
	Model    string      `xml:"modelName"`
	Services []service   `xml:"serviceList>service"`
	Children []deviceXML `xml:"deviceList>device"`
}
type description struct {
	XMLName xml.Name    `xml:"root"`
	Base    []string    `xml:"URLBase"`
	Devices []deviceXML `xml:"device"`
}

type endpoint struct {
	origin      netip.AddrPort
	control     string
	version     int
	name, model string
	until       time.Time
}

func label(s string) string {
	// Bound names by Unicode code points, not a slice through a UTF-8 sequence.
	r := make([]rune, 0, 100)
	for _, c := range strings.TrimSpace(s) {
		if !unicode.IsControl(c) && !unicode.Is(unicode.Cf, c) {
			r = append(r, c)
			if len(r) == cap(r) {
				break
			}
		}
	}
	return strings.TrimSpace(string(r))
}

func describe(ctx context.Context, l casting.LAN, c candidate) (endpoint, bool) {
	req, err := http.NewRequest(http.MethodGet, c.location.String(), nil)
	if err != nil {
		return endpoint{}, false
	}
	b, err := request(ctx, clientFor(l, c.origin), req, 256<<10)
	if err != nil || !validXML(b) {
		return endpoint{}, false
	}
	var desc description
	if xml.Unmarshal(b, &desc) != nil || (desc.XMLName.Space != "" && desc.XMLName.Space != "urn:schemas-upnp-org:device-1-0") || len(desc.Base) > 1 || len(desc.Devices) != 1 {
		return endpoint{}, false
	}
	base := c.location
	if len(desc.Base) == 1 && strings.TrimSpace(desc.Base[0]) != "" {
		var ok bool
		base, _, ok = checkedURL(strings.TrimSpace(desc.Base[0]), c.location, c.origin.Addr())
		if !ok {
			return endpoint{}, false
		}
	}
	var best endpoint
	var walk func(deviceXML)
	walk = func(d deviceXML) {
		if version(d.Type, rendererURN) > 0 {
			for _, s := range d.Services {
				v := version(s.Type, transportURN)
				if v == 0 {
					continue
				}
				u, _, ok := checkedURL(strings.TrimSpace(s.Control), base, c.origin.Addr())
				if !ok {
					continue
				}
				e := endpoint{c.origin, u.String(), v, label(d.Name), label(d.Model), c.until}
				if better(e, best) {
					best = e
				}
			}
		}
		for _, child := range d.Children {
			walk(child)
		}
	}
	walk(desc.Devices[0])
	return best, best.version != 0
}

// One address/port is one selectable public device (the public interface has no
// UDN/control path). For duplicate descriptions prefer the most informative one.
func better(a, b endpoint) bool {
	if b.version == 0 {
		return true
	}
	if (a.name != "") != (b.name != "") {
		return a.name != ""
	}
	if (a.model != "") != (b.model != "") {
		return a.model != ""
	}
	if a.version != b.version {
		return a.version > b.version
	}
	if a.control != b.control {
		return a.control < b.control
	}
	if a.name != b.name {
		return a.name < b.name
	}
	return a.model < b.model
}

type peerKey struct {
	iface  int
	local  netip.AddrPort
	prefix netip.Prefix
	remote netip.AddrPort
}

func key(l casting.LAN, remote netip.AddrPort) peerKey {
	return peerKey{l.Interface.Index, netip.AddrPortFrom(l.Addr, l.Port), l.Prefix.Masked(), remote}
}

type Transport struct {
	mu    sync.Mutex
	cache map[peerKey]endpoint
}

var _ casting.Transport = (*Transport)(nil)

func New() *Transport { return &Transport{cache: make(map[peerKey]endpoint)} }

func (t *Transport) Discover(ctx context.Context, l casting.LAN) ([]casting.Device, error) {
	cs, err := search(ctx, l)
	if err != nil {
		return nil, err
	}
	// SSDP collection is complete before starting any HTTP work. A single slow
	// receiver cannot hold discovery open, nor can many replies spawn many dials.
	work, cancel := context.WithTimeout(ctx, 3500*time.Millisecond)
	defer cancel()
	jobs := make(chan candidate, len(cs))
	out := make(chan endpoint, len(cs))
	for _, c := range cs {
		jobs <- c
	}
	close(jobs)
	var wg sync.WaitGroup
	for i := 0; i < min(4, len(cs)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				if work.Err() != nil {
					return
				}
				if e, ok := describe(work, l, c); ok {
					out <- e
				}
			}
		}()
	}
	wg.Wait()
	close(out)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	byOrigin := make(map[netip.AddrPort]endpoint)
	now := time.Now()
	for e := range out {
		if e.until.After(now) && better(e, byOrigin[e.origin]) {
			byOrigin[e.origin] = e
		}
	}
	return t.remember(l, byOrigin, now), nil
}

func (t *Transport) remember(l casting.LAN, endpoints map[netip.AddrPort]endpoint, now time.Time) []casting.Device {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cache == nil {
		t.cache = make(map[peerKey]endpoint)
	}
	for k, e := range t.cache {
		if !e.until.After(now) {
			delete(t.cache, k)
		}
	}
	ds := make([]casting.Device, 0, len(endpoints))
	for _, e := range endpoints {
		d := casting.Device{Protocol: casting.ProtocolDLNA, Addr: e.origin.Addr(), Port: int(e.origin.Port()), Model: e.model, Name: e.name}
		if d.Name == "" {
			d.Name = d.Model
		}
		if d.Name == "" {
			d.Name = "DLNA MediaRenderer"
		}
		ds = append(ds, d)
		t.cache[key(l, e.origin)] = e
	}
	for len(t.cache) > 128 {
		var victim peerKey
		var first time.Time
		for k, e := range t.cache {
			if first.IsZero() || e.until.Before(first) {
				victim, first = k, e.until
			}
		}
		delete(t.cache, victim)
	}
	sort.Slice(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Addr != b.Addr {
			return a.Addr.Less(b.Addr)
		}
		return a.Port < b.Port
	})
	return ds
}

func (t *Transport) lookup(l casting.LAN, d casting.Device) (endpoint, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.cache[key(l, netip.AddrPortFrom(d.Addr, uint16(d.Port)))]
	return e, ok && time.Now().Before(e.until)
}

func (t *Transport) resolve(ctx context.Context, l casting.LAN, d casting.Device, discover func(context.Context, casting.LAN) ([]casting.Device, error)) (endpoint, error) {
	if !validLAN(l) || d.Protocol != casting.ProtocolDLNA || !d.Addr.Is4() || !d.AllowedOn(l) {
		return endpoint{}, errTarget
	}
	if e, ok := t.lookup(l, d); ok {
		return e, nil
	}
	// A browser device ID can outlive SSDP max-age. Re-discover once, never
	// dial the caller's endpoint or reuse the expired control URL. Only the
	// exact scoped cache entry populated from a validated description counts.
	if _, err := discover(ctx, l); err != nil {
		return endpoint{}, err
	}
	if e, ok := t.lookup(l, d); ok {
		return e, nil
	}
	return endpoint{}, errTarget
}
