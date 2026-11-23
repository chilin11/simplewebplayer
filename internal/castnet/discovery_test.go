package castnet

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
	"simplewebplayer/internal/casting"
)

var testLAN = casting.LAN{Interface: net.Interface{Index: 9}, Addr: netip.MustParseAddr("192.168.42.1"), Prefix: netip.MustParsePrefix("192.168.42.0/24"), Port: 8080}

const testInstance = "Living-Room._googlecast._tcp.local."
const testHost = "Living-Room.local."

func rrHeader(name string, typ uint16) dns.RR_Header {
	return dns.RR_Header{Name: name, Rrtype: typ, Class: dns.ClassINET | 0x8000, Ttl: 10}
}

func TestSplitDiscoveryEvidence(t *testing.T) {
	now := time.Now()
	ip := netip.MustParseAddr("192.168.42.37")
	other := netip.MustParseAddr("192.168.42.38")
	s := scan{}
	ptr := &dns.PTR{Hdr: rrHeader(castService, dns.TypePTR), Ptr: testInstance}
	srv := &dns.SRV{Hdr: rrHeader(testInstance, dns.TypeSRV), Target: testHost, Port: casting.DevicePort}
	txt := &dns.TXT{Hdr: rrHeader(testInstance, dns.TypeTXT), Txt: []string{"fn=客厅电视", "md=Google TV"}}
	a := &dns.A{Hdr: rrHeader(testHost, dns.TypeA), A: ip.AsSlice()}
	accept := func(from netip.Addr, rs ...dns.RR) {
		m := dns.Msg{MsgHdr: dns.MsgHdr{Response: true}, Answer: rs}
		wire, err := m.Pack()
		if err != nil {
			t.Fatal(err)
		}
		var msg dns.Msg
		if err := msg.Unpack(wire); err != nil {
			t.Fatal(err)
		}
		s.accept(testLAN, from, &msg, now)
	}
	assertQuery := func(name string, typ uint16, want bool) {
		found := false
		for _, q := range s.questions(now, map[dns.Question]int{}, 1) {
			if q.Name == name && q.Qtype == typ {
				found = true
			}
		}
		if found != want {
			t.Fatalf("unexpected follow-up query for %s (%d)", name, typ)
		}
	}
	accept(ip, ptr)
	assertQuery(instanceName(testInstance), dns.TypeTXT, true)
	assertQuery(instanceName(testInstance), dns.TypeSRV, true)
	srv.Target = "example.org."
	accept(ip, srv)
	assertQuery("example.org.", dns.TypeA, false)
	srv.Target = testHost
	accept(ip, srv, txt)
	assertQuery(hostName(testHost), dns.TypeA, true)
	accept(other, a)
	if len(s.results(now)) != 0 {
		t.Fatal("joined A records across senders")
	}
	a.A = other.AsSlice()
	accept(ip, a)
	if len(s.results(now)) != 0 {
		t.Fatal("trusted foreign A address")
	}
	a.A = ip.AsSlice()
	accept(ip, a)
	ds := s.results(now)
	if len(ds) != 1 || ds[0].device != (casting.Device{Addr: ip, Port: 8009, Name: "客厅电视", Model: "Google TV"}) {
		t.Fatal("incomplete device or TXT decoding")
	}
	srv.Port = 8008
	accept(ip, srv)
	if len(s.results(now)) != 0 {
		t.Fatal("alternate port accepted")
	}
	srv.Port = 8009
	accept(ip, srv)
	ptr.Hdr.Ttl = 0
	accept(ip, ptr)
	if len(s.results(now)) != 0 {
		t.Fatal("goodbye retained")
	}
	ptr.Hdr.Ttl = 1
	accept(ip, ptr)
	if len(s.results(now.Add(1*time.Second))) != 0 {
		t.Fatal("expired PTR retained")
	}
}

func TestDiscoveryKeepsFriendlyMetadataForSharedEndpoint(t *testing.T) {
	now := time.Now()
	ip := netip.MustParseAddr("192.168.42.37")
	s := scan{}
	const alias = "alias._googlecast._tcp.local."
	const fallback = "aa-device.local."
	const friendly = "living room TV"
	m := &dns.Msg{MsgHdr: dns.MsgHdr{Response: true}, Answer: []dns.RR{
		&dns.PTR{Hdr: rrHeader(castService, dns.TypePTR), Ptr: alias},
		&dns.PTR{Hdr: rrHeader(castService, dns.TypePTR), Ptr: testInstance},
	}, Extra: []dns.RR{
		&dns.A{Hdr: rrHeader(fallback, dns.TypeA), A: ip.AsSlice()},
		&dns.SRV{Hdr: rrHeader(alias, dns.TypeSRV), Target: fallback, Port: casting.DevicePort},
		&dns.SRV{Hdr: rrHeader(testInstance, dns.TypeSRV), Target: fallback, Port: casting.DevicePort},
		&dns.TXT{Hdr: rrHeader(testInstance, dns.TypeTXT), Txt: []string{"fn=" + friendly, "md=Google TV"}},
	}}
	s.accept(testLAN, ip, m, now)
	out := s.results(now)
	if len(out) != 1 || out[0].device.Name != friendly || out[0].device.Model != "Google TV" {
		t.Fatal("fallback hostname masked live TXT metadata")
	}
}

func TestDiscoveryLimitAndSubnet(t *testing.T) {
	s := scan{}
	now := time.Now()
	for i := 2; i <= 60; i++ {
		ip := netip.AddrFrom4([4]byte{192, 168, 42, byte(i)})
		m := &dns.Msg{MsgHdr: dns.MsgHdr{Response: true}, Answer: []dns.RR{&dns.PTR{Hdr: rrHeader(castService, dns.TypePTR), Ptr: testInstance}}, Extra: []dns.RR{&dns.SRV{Hdr: rrHeader(testInstance, dns.TypeSRV), Port: 8009, Target: testHost}, &dns.A{Hdr: rrHeader(testHost, dns.TypeA), A: ip.AsSlice()}}}
		s.accept(testLAN, ip, m, now)
		s.accept(testLAN, netip.MustParseAddr("192.168.43.2"), m, now)
	}
	out := s.results(now)
	if len(out) != 32 || len(s) != 59 {
		t.Fatal("scan did not enforce max devices or subnet")
	}
}

func TestLocalOnlyBeforeDiscoveryOrDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tr := New()
	peer := casting.Device{Addr: netip.MustParseAddr("192.168.42.37"), Port: 8009}
	media := casting.Media{URL: "http://192.168.42.1:8080/cast/opaque", ContentType: "video/mp4"}
	if err := tr.Load(ctx, testLAN, peer, media); !errors.Is(err, context.Canceled) {
		t.Fatal("undiscovered target did not require discovery")
	}
	peer.Port = 8443
	if err := tr.Load(ctx, testLAN, peer, media); !errors.Is(err, errTarget) {
		t.Fatal("alternate port accepted")
	}
	peer.Port = 8009
	peer.Addr = netip.MustParseAddr("1.1.1.1")
	if err := tr.Load(ctx, testLAN, peer, media); !errors.Is(err, errTarget) {
		t.Fatal("outside IP accepted")
	}
	if err := tr.Stop(ctx, testLAN, peer, "http://example.invalid/cast/opaque"); !errors.Is(err, errMedia) {
		t.Fatal("outside media accepted")
	}
}
