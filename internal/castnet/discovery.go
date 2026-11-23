// Package castnet implements the IPv4 mDNS and TLS Cast v2 sender transports.
package castnet

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"

	"simplewebplayer/internal/casting"
)

const (
	castService   = "_googlecast._tcp.local."
	searchWindow  = 2500 * time.Millisecond
	queryInterval = 400 * time.Millisecond
	maxPacket     = 9000
	maxRecords    = 96 // per packet, before DNS unpacking
	maxDevices    = 32
)

var group = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
var errDiscovery = errors.New("cast: local discovery unavailable")

func validLAN(l casting.LAN) bool {
	return l.Interface.Index > 0 && l.Addr.Is4() && l.Addr.IsPrivate() && l.Prefix.Addr().Is4() && l.Prefix.Bits() > 0 && l.Prefix.Bits() <= 30 && l.Prefix.Contains(l.Addr)
}

type instance struct {
	ptr, srv, txt     time.Time
	host, name, model string
}

type responder struct {
	instances map[string]*instance
	addresses map[string]time.Time // Only A records equal to this responder's source IP.
}

type scan map[netip.Addr]*responder

type advertisement struct {
	device casting.Device
	until  time.Time
}

func instanceName(s string) string {
	s = strings.ToLower(s)
	if len(s) > 256 || dns.CountLabel(s) != 4 || !strings.HasSuffix(s, "."+castService) {
		return ""
	}
	if _, ok := dns.IsDomainName(s); !ok {
		return ""
	}
	return s
}

// Only query SRV targets that are actual host names under .local., never an
// address from another DNS zone or arbitrary names learned from other RRs.
func hostName(s string) string {
	s = strings.ToLower(s)
	if len(s) > 254 || !strings.HasSuffix(s, ".local.") {
		return ""
	}
	for _, label := range strings.Split(strings.TrimSuffix(s, "."), ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return ""
			}
		}
	}
	return s
}

func display(s string) string {
	// miekg/dns exposes TXT in DNS presentation format, including decimal
	// escapes for every non-ASCII octet. Restore the original UTF-8 first.
	var raw strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			if i+3 < len(s) {
				if v, err := strconv.ParseUint(s[i+1:i+4], 10, 8); err == nil {
					raw.WriteByte(byte(v))
					i += 3
					continue
				}
			}
			i++
		}
		raw.WriteByte(s[i])
	}
	s = strings.ToValidUTF8(raw.String(), "")
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
}

func (r *responder) instance(s string) *instance {
	k := instanceName(s)
	if k == "" {
		return nil
	}
	v := r.instances[k]
	if v == nil && len(r.instances) < 24 {
		v = &instance{}
		r.instances[k] = v
	}
	return v
}

func (s scan) accept(l casting.LAN, ip netip.Addr, m *dns.Msg, now time.Time) {
	if !l.Contains(ip) || !m.Response || m.Opcode != dns.OpcodeQuery || m.Rcode != dns.RcodeSuccess || m.Truncated {
		return
	}
	r := s[ip]
	for _, section := range [][]dns.RR{m.Answer, m.Extra} {
		for _, rr := range section {
			h := rr.Header()
			if h.Class&0x7fff != dns.ClassINET {
				continue
			}
			if r == nil {
				if len(s) >= 96 {
					return
				}
				r = &responder{map[string]*instance{}, map[string]time.Time{}}
				s[ip] = r
			}
			// Retain discovery provenance briefly even if the advertised TTL is days.
			until := now.Add(time.Duration(min(h.Ttl, uint32(120))) * time.Second)
			switch x := rr.(type) {
			case *dns.PTR:
				if strings.EqualFold(h.Name, castService) {
					if v := r.instance(x.Ptr); v != nil {
						v.ptr = until
					}
				}
			case *dns.SRV:
				if v := r.instance(h.Name); v != nil {
					v.host = hostName(x.Target)
					v.srv = until
					if x.Port != casting.DevicePort || v.host == "" {
						v.srv = time.Time{}
					}
				}
			case *dns.TXT:
				if v := r.instance(h.Name); v != nil {
					v.txt = until
					v.name = ""
					v.model = ""
					for _, kv := range x.Txt {
						k, val, ok := strings.Cut(kv, "=")
						if ok {
							switch strings.ToLower(k) {
							case "fn":
								v.name = display(val)
							case "md":
								v.model = display(val)
							}
						}
					}
				}
			case *dns.A:
				k := hostName(h.Name)
				a, ok := netip.AddrFromSlice(x.A)
				if ok && a.Unmap() == ip && k != "" {
					if _, exists := r.addresses[k]; exists || len(r.addresses) < 32 {
						r.addresses[k] = until
					}
				}
			}
		}
	}
}

func (s scan) results(now time.Time) []advertisement {
	out := make([]advertisement, 0, len(s))
	for ip, r := range s {
		var best advertisement
		bestRank := -1
		for _, v := range r.instances {
			until := v.ptr
			if v.srv.Before(until) {
				until = v.srv
			}
			if a := r.addresses[v.host]; a.Before(until) {
				until = a
			}
			if !until.After(now) {
				continue
			}
			name, model := "", ""
			if v.txt.After(now) {
				name = v.name
				model = v.model
			}
			// Several service instances may describe one TCP endpoint. A real
			// DNS-SD friendly name outranks a fallback host name, and model is
			// secondary. Never mix metadata or addresses between instances.
			rank := 0
			if name == "" {
				name = strings.TrimSuffix(v.host, ".local.")
			} else {
				rank += 2
			}
			if model != "" {
				rank++
			}
			d := casting.Device{Name: name, Model: model, Addr: ip, Port: casting.DevicePort}
			old := best.device
			prefer := rank > bestRank
			if rank == bestRank {
				switch {
				case name != old.Name:
					prefer = name < old.Name
				case model != old.Model:
					prefer = model < old.Model
				default:
					prefer = until.After(best.until)
				}
			}
			if prefer {
				bestRank, best = rank, advertisement{d, until}
			}
		}
		if best.device.Addr.IsValid() {
			out = append(out, best)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].device.Addr.Less(out[j].device.Addr) })
	return out[:min(len(out), maxDevices)]
}

// Ask for missing records in later rounds: older firmwares don't bundle all
// the additional records with their initial PTR answer. Max two attempts/RR.
func (s scan) questions(now time.Time, attempts map[dns.Question]int, round int) []dns.Question {
	var qs []dns.Question
	if round == 0 || round == 3 {
		qs = append(qs, dns.Question{Name: castService, Qtype: dns.TypePTR, Qclass: dns.ClassINET})
	}
	seen := map[dns.Question]bool{}
	add := func(name string, typ uint16) {
		q := dns.Question{Name: name, Qtype: typ, Qclass: dns.ClassINET}
		if len(qs) >= 48 || seen[q] || attempts[q] >= 2 {
			return
		}
		qs = append(qs, q)
		attempts[q]++
		seen[q] = true
	}
	for _, r := range s {
		for k, v := range r.instances {
			if !v.ptr.After(now) {
				continue
			}
			if !v.srv.After(now) {
				add(k, dns.TypeSRV)
			}
			if !v.txt.After(now) {
				add(k, dns.TypeTXT)
			}
			if v.srv.After(now) && !r.addresses[v.host].After(now) {
				add(v.host, dns.TypeA)
			}
		}
	}
	return qs
}

func scanLAN(ctx context.Context, l casting.LAN) ([]advertisement, error) {
	if !validLAN(l) {
		return nil, errDiscovery
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u, err := net.ListenMulticastUDP("udp4", &l.Interface, group)
	if err != nil {
		return nil, errDiscovery
	}
	defer u.Close()
	stop := context.AfterFunc(ctx, func() { u.Close() })
	defer stop()
	pc := ipv4.NewPacketConn(u)
	if pc.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true) != nil || pc.SetMulticastInterface(&l.Interface) != nil || pc.SetMulticastTTL(255) != nil {
		return nil, errDiscovery
	}
	cm := &ipv4.ControlMessage{IfIndex: l.Interface.Index, Src: net.IP(l.Addr.AsSlice())}
	s := scan{}
	attempts := map[dns.Question]int{}
	buf := make([]byte, maxPacket+1)
	end := time.Now().Add(searchWindow)
	if d, ok := ctx.Deadline(); ok && d.Before(end) {
		end = d
	}
	if err := u.SetWriteDeadline(end); err != nil {
		return nil, errDiscovery
	}
	var next time.Time
	for round, packets, records := 0, 0, 0; packets < 384 && records < 2048; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		now := time.Now()
		if !now.Before(end) {
			break
		}
		if !now.Before(next) {
			// Send individual questions; some old DNS-SD implementations only
			// process the first question in a multi-question packet.
			for _, q := range s.questions(now, attempts, round) {
				m := dns.Msg{Question: []dns.Question{q}}
				data, err := m.Pack()
				if err != nil {
					continue
				}
				if _, err = pc.WriteTo(data, cm, group); err != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					return nil, errDiscovery
				}
			}
			round++
			next = now.Add(queryInterval)
			if next.After(end) {
				next = end
			}
			if err := u.SetReadDeadline(next); err != nil {
				return nil, errDiscovery
			}
		}
		n, meta, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return nil, errDiscovery
		}
		packets++
		a, ok := from.(*net.UDPAddr)
		if !ok || a.Port != 5353 || meta == nil || meta.IfIndex != l.Interface.Index || !(meta.Dst.Equal(group.IP) || meta.Dst.Equal(cm.Src)) || n < 12 || n > maxPacket {
			continue
		}
		ip := a.AddrPort().Addr().Unmap()
		if !l.Contains(ip) {
			continue
		}
		count := 0
		for off := 4; off < 12; off += 2 {
			count += int(buf[off])<<8 | int(buf[off+1])
		}
		if count > maxRecords || count > 2048-records {
			continue
		}
		records += count
		var m dns.Msg
		if m.Unpack(buf[:n]) == nil {
			s.accept(l, ip, &m, time.Now())
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.results(time.Now()), nil
}
