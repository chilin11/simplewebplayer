package dlnanet

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"simplewebplayer/internal/casting"
)

var testLAN = casting.LAN{Interface: net.Interface{Index: 7}, Addr: netip.MustParseAddr("192.168.24.1"), Prefix: netip.MustParsePrefix("192.168.24.0/24"), Port: 8080}

func TestSSDPProvenance(t *testing.T) {
	peer := netip.MustParseAddr("192.168.24.5")
	loc := "http://192.168.24.5:49152/device.xml"
	now := time.Now()
	reply := func(st, where string) []byte {
		return []byte("HTTP/1.1 200 OK\r\nST: " + st + "\r\nLocation: " + where + "\r\nCache-Control: max-age=1800\r\n\r\n")
	}
	for _, st := range []string{rendererURN + "1", transportURN + "2"} {
		c, ok := fromReply(reply(st, loc), peer, testLAN, now)
		if !ok || c.origin != netip.AddrPortFrom(peer, 49152) || c.until.Sub(now) != 5*time.Minute {
			t.Fatal("renderer reply or expiry")
		}
	}
	for _, where := range []string{"http://renderer.local/x", "http://192.168.24.6/x", "http://[192.168.24.5]/x", "https://192.168.24.5/x", "http://x@192.168.24.5/x", "http://192.168.24.5:0/x", "http://192.168.24.5:/x", "http://192.168.24.5/x#fragment", loc + "\r\nLocation: " + loc} {
		if _, ok := fromReply(reply(rendererURN+"1", where), peer, testLAN, now); ok {
			t.Error("invalid LOCATION accepted", where)
		}
	}
	for _, ip := range []string{"192.168.24.1", "192.168.24.0", "192.168.24.255", "192.168.25.5", "127.0.0.1"} {
		p := netip.MustParseAddr(ip)
		if _, ok := fromReply(reply(rendererURN+"1", "http://"+ip+"/tv.xml"), p, testLAN, now); ok {
			t.Error("invalid UDP source accepted", ip)
		}
	}
	if _, ok := fromReply(reply("urn:schemas-upnp-org:device:MediaServer:1", loc), peer, testLAN, now); ok {
		t.Error("MediaServer reply accepted")
	}
}

// Only httptest loopback is used for HTTP. Production discovery obtains these
// private candidate values from recvfrom; no test calls live SSDP discovery.
func loopCandidate(t *testing.T, raw string) candidate {
	t.Helper()
	u, ap, ok := checkedURL(raw, nil, netip.MustParseAddr("127.0.0.1"))
	if !ok {
		t.Fatal("invalid loopback fixture")
	}
	return candidate{u, ap, time.Now().Add(time.Minute)}
}

var loopLAN = casting.LAN{Addr: netip.MustParseAddr("127.0.0.1")}

func TestRendererDescriptionAndPinnedOrigin(t *testing.T) {
	const serverType = "urn:schemas-upnp-org:device:MediaServer:1"
	device := func(kind, services, children string) string {
		return `<device><deviceType>` + kind + `</deviceType><friendlyName>客厅电视</friendlyName><modelName>OLED</modelName>` + services + children + `</device>`
	}
	svc := func(control string) string {
		return `<serviceList><service><serviceType>` + transportURN + `2</serviceType><controlURL>` + control + `</controlURL></service></serviceList>`
	}
	child := func(d string) string { return `<deviceList>` + d + `</deviceList>` }
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("cross-origin request reached trap") }))
	defer trap.Close()
	wrapped := func(base, dev string) string {
		return `<root xmlns="urn:schemas-upnp-org:device-1-0">` + base + dev + `</root>`
	}
	renderer := device(rendererURN+"2", svc("../AVTransport"), "")
	descs := map[string]string{
		"nested":          wrapped("<URLBase>/upnp/services/</URLBase>", device(serverType, svc("/iptv"), child(renderer))),
		"server":          wrapped("", device(serverType, svc("/server"), "")),
		"borrowed":        wrapped("", device(rendererURN+"1", "", child(device(serverType, svc("/wrong"), "")))),
		"foreign-base":    wrapped("<URLBase>"+trap.URL+"/upnp/</URLBase>", renderer),
		"foreign-control": wrapped("", device(rendererURN+"1", svc(trap.URL+"/evil"), "")),
		"large":           strings.Repeat(" ", 256<<10) + wrapped("", renderer),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("description method")
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, trap.URL, 302)
			return
		}
		_, _ = w.Write([]byte(descs[strings.TrimPrefix(r.URL.Path, "/")]))
	}))
	defer srv.Close()
	c := loopCandidate(t, srv.URL+"/nested")
	e, ok := describe(t.Context(), loopLAN, c)
	if !ok || e.version != 2 || e.name != "客厅电视" || e.model != "OLED" || e.control != srv.URL+"/upnp/AVTransport" || e.origin != c.origin {
		t.Fatal("valid nested renderer not selected")
	}
	for _, path := range []string{"server", "borrowed", "foreign-base", "foreign-control", "redirect", "large"} {
		if _, ok := describe(t.Context(), loopLAN, loopCandidate(t, srv.URL+"/"+path)); ok {
			t.Error("unsafe description accepted", path)
		}
	}
	cl := clientFor(loopLAN, c.origin)
	if resp, err := cl.Get(trap.URL); err == nil {
		resp.Body.Close()
		t.Error("client dialed another origin")
	}
}

func TestScopedCacheAndMedia(t *testing.T) {
	tr := New()
	ap := netip.MustParseAddrPort("192.168.24.5:49152")
	e := endpoint{origin: ap, name: "客厅", model: "webOS", version: 2, until: time.Now().Add(time.Minute)}
	ds := tr.remember(testLAN, map[netip.AddrPort]endpoint{ap: e}, time.Now())
	if len(ds) != 1 || ds[0].Protocol != casting.ProtocolDLNA || ds[0].Port != 49152 || ds[0].Name != e.name {
		t.Fatal("wrong public renderer")
	}
	d := ds[0]
	noDevices := func(context.Context, casting.LAN) ([]casting.Device, error) { return nil, nil }
	if _, err := tr.resolve(t.Context(), testLAN, d, noDevices); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*casting.LAN, *casting.Device){func(_ *casting.LAN, d *casting.Device) { d.Port++ }, func(_ *casting.LAN, d *casting.Device) { d.Protocol = "" }, func(l *casting.LAN, _ *casting.Device) { l.Port++ }, func(l *casting.LAN, _ *casting.Device) { l.Interface.Index++ }, func(l *casting.LAN, _ *casting.Device) { l.Prefix = netip.MustParsePrefix("192.168.24.0/25") }} {
		l, dev := testLAN, d
		change(&l, &dev)
		if _, err := tr.resolve(t.Context(), l, dev, noDevices); !errors.Is(err, errTarget) {
			t.Error("endpoint leaked across scope")
		}
	}
	uri := "http://" + testLAN.HTTPAddress() + "/api/cast/media/ticket"
	if !localMedia(testLAN, uri) {
		t.Error("valid local media rejected")
	}
	for _, raw := range []string{"http://192.168.24.5:8080/media", "https://" + testLAN.HTTPAddress() + "/media", uri + "#x", "http://user@" + testLAN.HTTPAddress() + "/media", "http://" + testLAN.HTTPAddress() + "//other.host/path"} {
		if localMedia(testLAN, raw) {
			t.Error("invalid media accepted")
		}
	}
	e.until = time.Now().Add(-time.Second)
	tr.cache[key(testLAN, ap)] = e
	if _, ok := tr.lookup(testLAN, d); ok {
		t.Error("expired entry used")
	}
	// Public Load and Stop both require live discovery after expiry. Cancel
	// before the scanner opens a socket: these tests never send LAN traffic.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := tr.Load(ctx, testLAN, d, casting.Media{URL: uri, ContentType: "video/mp4"}); !errors.Is(err, context.Canceled) {
		t.Error("expired load did not require rediscovery")
	}
	if err := tr.Stop(ctx, testLAN, d, uri); !errors.Is(err, context.Canceled) {
		t.Error("expired stop did not require rediscovery")
	}

	// The old port is gone; the same address now advertises only a different
	// port. A returned device ID (or the stale cached URL) is not provenance.
	calls := 0
	discover := func(_ context.Context, l casting.LAN) ([]casting.Device, error) {
		calls++
		if key(l, ap) != key(testLAN, ap) {
			t.Fatal("refreshed wrong LAN")
		}
		other := e
		other.origin = netip.AddrPortFrom(ap.Addr(), ap.Port()+1)
		other.until = time.Now().Add(time.Minute)
		tr.remember(l, map[netip.AddrPort]endpoint{other.origin: other}, time.Now())
		return []casting.Device{d}, nil
	}
	if _, err := tr.resolve(t.Context(), testLAN, d, discover); !errors.Is(err, errTarget) || calls != 1 {
		t.Fatal("substituted or stale endpoint accepted after refresh")
	}
	fresh := e
	fresh.until = time.Now().Add(time.Minute)
	fresh.control = "http://" + ap.String() + "/new-path"
	got, err := tr.resolve(t.Context(), testLAN, d, func(_ context.Context, l casting.LAN) ([]casting.Device, error) {
		return tr.remember(l, map[netip.AddrPort]endpoint{ap: fresh}, time.Now()), nil
	})
	if err != nil || got.control != fresh.control {
		t.Error("exact rediscovered endpoint was not used")
	}
}

func TestRelativeControlURLs(t *testing.T) {
	ip := netip.MustParseAddr("192.168.24.5")
	base, _, _ := checkedURL("http://192.168.24.5:80/dir/tv.xml", nil, ip)
	for _, raw := range []string{"http://user@192.168.24.5/path", "//foreign.local/foo", "//192.168.24.5:8333/foo", "https://192.168.24.5/foo", "file:///dev/null", "http:opaque", "#foo"} {
		if _, _, ok := checkedURL(raw, base, ip); ok {
			t.Error("accepted control ref", raw)
		}
	}
	for _, raw := range []string{"/ctrl", "../ctrl", "http://192.168.24.5/ctrl"} {
		u, _, ok := checkedURL(raw, base, ip)
		if !ok || u.String() != "http://192.168.24.5:80/ctrl" {
			t.Error("valid control ref rejected", raw)
		}
	}
}
