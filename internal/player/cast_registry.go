package player

import (
	"crypto/rand"
	"encoding/base64"
	"net/netip"
	"sync"
	"time"

	"simplewebplayer/internal/casting"
)

const (
	castDeviceTTL    = 2 * time.Minute
	castStopTTL      = 2 * time.Minute
	castAccountLimit = 16
	castGlobalLimit  = 256
	castVisibleLimit = 32
	castTicketTTL    = 6 * time.Hour
	castMediaPath    = "/api/cast/media/"
)

// Index, address, network and HTTP listener must all still match. Never use
// the request Host (even when it happens to be an IP address).
type castNetwork struct {
	index  int
	addr   netip.Addr
	prefix netip.Prefix
	port   uint16
}

func networkOf(l casting.LAN) castNetwork {
	return castNetwork{l.Interface.Index, l.Addr, l.Prefix.Masked(), l.Port}
}

type castOwner struct {
	account int64
	session string // binary session digest, not the browser credential
	network castNetwork
}

type discoveredDevice struct {
	id      string
	device  casting.Device
	expires time.Time
}

// Serialize commands to one physical address, including across protocols and ports.
type castReceiver struct {
	iface int
	addr  netip.Addr
}

type castEndpoint struct {
	protocol string
	address  netip.AddrPort
}

func endpoint(d casting.Device) castEndpoint {
	return castEndpoint{d.Kind(), netip.AddrPortFrom(d.Addr, uint16(d.Port))}
}

type castTicket struct {
	owner                  castOwner
	device                 casting.Device
	root                   int64
	path, token, controlID string
	expires                time.Time
	revoked                bool // control remains usable until expires; media never does
}

func (t castTicket) receiver() castReceiver {
	return castReceiver{t.owner.network.index, t.device.Addr}
}

func (t castTicket) mediaURL(l casting.LAN) string {
	return "http://" + l.HTTPAddress() + castMediaPath + t.token
}

type castRegistry struct {
	mu        sync.Mutex
	choices   map[castOwner]map[castEndpoint]discoveredDevice
	operating map[castReceiver]bool // network call in flight; unaffected by logout or expiry
	tickets   map[string]castTicket // media token; control IDs are independent
}

func newCastRegistry() castRegistry {
	return castRegistry{choices: make(map[castOwner]map[castEndpoint]discoveredDevice), tickets: make(map[string]castTicket), operating: make(map[castReceiver]bool)}
}

func castID() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// All registry operations are short and never call SQL, SMB or Cast transport.
func (s *castRegistry) prune(now time.Time) {
	for k, devices := range s.choices {
		for endpoint, d := range devices {
			if !now.Before(d.expires) {
				delete(devices, endpoint)
			}
		}
		if len(devices) == 0 {
			delete(s.choices, k)
		}
	}
	for k, t := range s.tickets {
		if !now.Before(t.expires) {
			delete(s.tickets, k)
		}
	}
}

// Merge this (already filtered and deduplicated) scan. Do not refresh or
// advertise absent devices. At capacity fail rather than invalidate other tabs.
func (s *castRegistry) remember(o castOwner, devices []casting.Device) ([]publicDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.prune(now)
	out := make([]publicDevice, 0, len(devices))
	if len(devices) == 0 {
		return out, nil
	}
	choices := s.choices[o]
	if choices == nil {
		owners := 0
		for owner := range s.choices {
			if owner.account == o.account {
				owners++
			}
		}
		if owners >= castAccountLimit || len(s.choices) >= 128 {
			return nil, castBusy()
		}
		choices = make(map[castEndpoint]discoveredDevice)
	}
	n := len(choices)
	for _, d := range devices {
		if _, ok := choices[endpoint(d)]; !ok {
			n++
		}
	}
	if n > 2*castVisibleLimit {
		return nil, castBusy()
	}
	for _, d := range devices {
		key := endpoint(d)
		id := choices[key].id
		if id == "" {
			id = castID()
		}
		choices[key] = discoveredDevice{id, d, now.Add(castDeviceTTL)}
		out = append(out, publicDevice{ID: id, Name: d.Name, Model: d.Model, Protocol: key.protocol})
	}
	s.choices[o] = choices
	return out, nil
}

func (s *castRegistry) issue(o castOwner, deviceID string, root int64, path string, sessionExpiry int64) (castTicket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.prune(now)
	var choice discoveredDevice
	for _, d := range s.choices[o] {
		if d.id == deviceID {
			choice = d
			break
		}
	}
	if choice.id == "" || sessionExpiry <= now.Unix() {
		return castTicket{}, missing()
	}
	count := 0
	for _, t := range s.tickets {
		if t.owner.account == o.account {
			count++
		}
	}
	if count >= castAccountLimit || len(s.tickets) >= castGlobalLimit || len(s.operating) >= castGlobalLimit {
		return castTicket{}, castBusy()
	}
	t := castTicket{owner: o, device: choice.device, root: root, path: path, token: castID(), controlID: castID(), expires: time.Unix(min(sessionExpiry, now.Add(castTicketTTL).Unix()), 0)}
	if s.operating[t.receiver()] {
		return castTicket{}, deviceBusy()
	}
	s.operating[t.receiver()] = true
	s.tickets[t.token] = t
	return t, nil
}

func (s *castRegistry) lookup(token string) (castTicket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[token]
	if ok && !time.Now().Before(t.expires) {
		delete(s.tickets, token)
		ok = false
	}
	return t, ok && !t.revoked
}

func (s *castRegistry) finishLoad(t castTicket, loaded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.operating, t.receiver())
	if !loaded {
		delete(s.tickets, t.token)
		return
	}
	// A successful LOAD replaces the URL on this transport endpoint only.
	// Independent receiver apps at the same IP may still be streaming.
	for k, old := range s.tickets {
		if k != t.token && old.owner.network.index == t.owner.network.index && endpoint(old.device) == endpoint(t.device) {
			delete(s.tickets, k)
		}
	}
}

func (s *castRegistry) revokeMedia(t castTicket, now time.Time) {
	if t.revoked {
		return
	}
	t.revoked = true
	if until := now.Add(castStopTTL); until.Before(t.expires) {
		t.expires = until
	}
	s.tickets[t.token] = t
}

func (s *castRegistry) loseGrant(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tickets[token]; ok {
		s.revokeMedia(t, time.Now())
	}
}

func (s *castRegistry) beginStop(o castOwner, id string) (castTicket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.prune(now)
	for _, t := range s.tickets {
		if t.controlID == id && t.owner == o {
			s.revokeMedia(t, now)
			if s.operating[t.receiver()] {
				return castTicket{}, deviceBusy()
			}
			s.operating[t.receiver()] = true
			return t, nil
		}
	}
	return castTicket{}, missing()
}

func (s *castRegistry) finishStop(t castTicket, stopped bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.operating, t.receiver())
	if stopped {
		delete(s.tickets, t.token)
	}
}

func (s *castRegistry) forgetSession(hash []byte) {
	if len(hash) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for o := range s.choices {
		if o.session == string(hash) {
			delete(s.choices, o)
		}
	}
	for k, t := range s.tickets {
		if t.owner.session == string(hash) {
			delete(s.tickets, k)
		}
	}
}

// Snapshot session digests while SQLite excludes new logins. Release only these
// after commit, never a fresh session opened between commit and this callback.
func (s *castRegistry) onAccountReset(id int64) func() error {
	s.mu.Lock()
	hashes := make(map[string]bool)
	for owner := range s.choices {
		if owner.account == id {
			hashes[owner.session] = true
		}
	}
	for _, t := range s.tickets {
		if t.owner.account == id {
			hashes[t.owner.session] = true
		}
	}
	s.mu.Unlock()
	return func() error {
		for h := range hashes {
			s.forgetSession([]byte(h))
		}
		return nil
	}
}

func deviceBusy() error {
	return &apiError{409, "cast_device_busy", "电视正在处理另一条投屏指令，请稍后重试"}
}

func castBusy() error { return &apiError{429, "cast_busy", "投屏请求过多，请稍后再试"} }
