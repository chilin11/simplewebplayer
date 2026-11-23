package player

import (
	"context"
	"encoding/json"
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

type testCastTransport struct {
	devices []casting.Device
	load    func(casting.Device, casting.Media) error
	stop    func(casting.Device, string) error
}

func (f *testCastTransport) Discover(context.Context, casting.LAN) ([]casting.Device, error) {
	return f.devices, nil
}
func (f *testCastTransport) Load(_ context.Context, _ casting.LAN, d casting.Device, m casting.Media) error {
	return f.load(d, m)
}
func (f *testCastTransport) Stop(_ context.Context, _ casting.LAN, d casting.Device, url string) error {
	return f.stop(d, url)
}

func TestCastRegistryDeadlines(t *testing.T) {
	s := newCastRegistry()
	o := castOwner{session: "test session"}
	d := casting.Device{Addr: netip.MustParseAddr("192.168.1.10"), Port: 8009}
	choices, err := s.remember(o, []casting.Device{d})
	if err != nil {
		t.Fatal(err)
	}
	id := choices[0].ID
	deadline := s.choices[o][endpoint(d)].expires
	d2 := d
	d2.Addr = netip.MustParseAddr("192.168.1.11")
	other, err := s.remember(o, []casting.Device{d2})
	if err != nil || len(other) != 1 || other[0].ID == id || s.choices[o][endpoint(d)].expires != deadline {
		t.Fatal("absent devices were advertised or refreshed")
	}
	again, err := s.remember(o, []casting.Device{d})
	if err != nil || len(again) != 1 || again[0].ID != id {
		t.Fatal("rediscovered device lost its opaque ID")
	}
	now := time.Now()
	sessionEnd := now.Add(time.Minute).Unix()
	ticket, err := s.issue(o, id, 1, "x.mp4", sessionEnd)
	if err != nil || ticket.expires.Unix() != sessionEnd {
		t.Fatal("ticket outlived session")
	}
	s.finishLoad(ticket, true)
	long, err := s.issue(o, id, 1, "x.mp4", now.Add(24*time.Hour).Unix())
	if err != nil || long.expires.After(time.Now().Add(6*time.Hour)) {
		t.Fatal("missing hard ticket deadline")
	}
	s.finishLoad(long, false)
	if _, ok := s.lookup(ticket.token); !ok {
		t.Fatal("failed replacement revoked old media")
	}
	if _, ok := s.lookup(long.token); ok {
		t.Fatal("failed replacement still authorized")
	}
	stop, e := s.beginStop(o, ticket.controlID)
	if e != nil {
		t.Fatal(e)
	}
	s.finishStop(stop, false)
	retryExpiry := s.tickets[ticket.token].expires
	if retryExpiry.Unix() > sessionEnd || retryExpiry.After(time.Now().Add(castStopTTL)) {
		t.Fatal("retry escaped original deadline")
	}
	stop, e = s.beginStop(o, ticket.controlID)
	if e != nil {
		t.Fatal(e)
	}
	s.finishStop(stop, false)
	if s.tickets[ticket.token].expires != retryExpiry {
		t.Fatal("retry extended deadline")
	}
	ticket.expires = now
	s.tickets[ticket.token] = ticket
	if _, ok := s.lookup(ticket.token); ok {
		t.Fatal("expired ticket accepted")
	}
	old := s.choices[o][endpoint(d)]
	old.expires = now
	s.choices[o][endpoint(d)] = old
	if _, err := s.issue(o, id, 1, "x.mp4", sessionEnd); err == nil {
		t.Fatal("expired discovery accepted")
	}
}

func TestCastRegistryDualProtocol(t *testing.T) {
	s := newCastRegistry()
	o := castOwner{account: 1, session: "same browser"}
	google := casting.Device{Addr: netip.MustParseAddr("192.168.1.10"), Port: 8009}
	dlna := google
	dlna.Protocol = casting.ProtocolDLNA
	otherPort := dlna
	otherPort.Port = 80
	devices, err := s.remember(o, []casting.Device{google, dlna, otherPort})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.remember(o, []casting.Device{dlna, google})
	if err != nil || again[0].ID != devices[1].ID || again[1].ID != devices[0].ID {
		t.Fatal("IDs were not stable across scan order")
	}
	issue := func(i int) (castTicket, error) {
		return s.issue(o, devices[i].ID, 1, "a.mp4", time.Now().Add(time.Hour).Unix())
	}
	g, err := issue(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{1, 2} {
		if _, err := issue(i); err == nil {
			t.Fatal("overlapping LOAD on same TV")
		}
	}
	s.finishLoad(g, true)
	d, err := issue(1)
	if err != nil {
		t.Fatal(err)
	}
	s.finishLoad(d, true)
	if _, ok := s.lookup(g.token); !ok {
		t.Fatal("DLNA displaced independent Google receiver")
	}
	stop, err := s.beginStop(o, g.controlID)
	if err != nil || stop.token != g.token || stop.device.Kind() != casting.ProtocolGoogle {
		t.Fatal("stop targeted the wrong protocol or media")
	}
	if _, err := issue(2); err == nil {
		t.Fatal("DLNA LOAD overlapped Google STOP")
	}
	s.finishStop(stop, true)
	if _, ok := s.lookup(d.token); !ok {
		t.Fatal("stopping Google receiver revoked DLNA media")
	}
}

func TestCastAccountQuota(t *testing.T) {
	s := newCastRegistry()
	expiry := time.Now().Add(time.Hour).Unix()
	type choice struct {
		o  castOwner
		id string
	}
	var selections []choice
	for i := 0; i <= castAccountLimit; i++ {
		o := castOwner{account: 1, session: "a"}
		if i%2 == 1 {
			o.session = "b"
		}
		d := casting.Device{Addr: netip.AddrFrom4([4]byte{192, 168, 1, byte(2 + i)}), Port: 8009}
		choices, e := s.remember(o, []casting.Device{d})
		if e != nil {
			t.Fatal(e)
		}
		selections = append(selections, choice{o, choices[0].ID})
	}
	issue := func(c choice) (castTicket, error) { return s.issue(c.o, c.id, 1, "x.mp4", expiry) }
	for _, c := range selections[:castAccountLimit] {
		ticket, e := issue(c)
		if e != nil {
			t.Fatal(e)
		}
		s.finishLoad(ticket, true)
	}
	last := selections[castAccountLimit]
	if _, e := issue(last); e == nil {
		t.Fatal("quota bypassed across sessions")
	}
	o := castOwner{account: 2, session: "c"}
	devices, e := s.remember(o, []casting.Device{{Addr: netip.MustParseAddr("192.168.1.200"), Port: 8009}})
	if e != nil {
		t.Fatal(e)
	}
	ticket, e := issue(choice{o, devices[0].ID})
	if e != nil {
		t.Fatal("second account denied", e)
	}
	s.finishLoad(ticket, true)
	s.forgetSession([]byte("b"))
	if _, e := issue(last); e != nil {
		t.Fatal("failed to free logged-out session quota", e)
	}
}

func TestCastAuthorization(t *testing.T) {
	for i, protocol := range []string{casting.ProtocolGoogle, casting.ProtocolDLNA} {
		t.Run(protocol, func(t *testing.T) { testCastAuthorization(t, i, protocol) })
	}
}

func testCastAuthorization(t *testing.T, deviceIndex int, protocol string) {
	const start, discover, stop = "/api/library/1/cast", "/api/cast/discover", "/api/cast/stop"
	const tv = "192.168.20.42"
	const file = "我的电影/a.M4V"
	fake := &fakeMedia{stream: func(w http.ResponseWriter, r *http.Request, path string) error {
		if path != file {
			t.Fatal("stream was not bound to selected file")
		}
		http.ServeContent(w, r, "a.m4v", time.Time{}, strings.NewReader("abcdefgh"))
		return nil
	}}
	a, admin := setup(t, t.TempDir(), fake)
	transport := &testCastTransport{devices: []casting.Device{
		{Name: "remote", Addr: netip.MustParseAddr("192.168.19.2"), Port: 8009},
		{Name: "wrong port", Addr: netip.MustParseAddr(tv), Port: 8008},
		{Name: "TV", Model: "Receiver", Addr: netip.MustParseAddr(tv), Port: 8009},
		{Name: "duplicate", Protocol: casting.ProtocolGoogle, Addr: netip.MustParseAddr(tv), Port: 8009},
		{Name: "TV", Protocol: casting.ProtocolDLNA, Addr: netip.MustParseAddr(tv), Port: 8009},
		{Name: "duplicate", Protocol: casting.ProtocolDLNA, Addr: netip.MustParseAddr(tv), Port: 8009},
		{Name: "other endpoint", Protocol: casting.ProtocolDLNA, Addr: netip.MustParseAddr(tv), Port: 49152},
		{Name: "unknown", Protocol: "unknown", Addr: netip.MustParseAddr(tv), Port: 8009},
		{Name: "invalid", Protocol: casting.ProtocolDLNA, Addr: netip.MustParseAddr(tv), Port: 65536},
		{Name: "off LAN", Protocol: casting.ProtocolDLNA, Addr: netip.MustParseAddr("192.168.19.2"), Port: 80},
	}}
	want(t, req(a, admin, "POST", discover, "{}"), 503)
	a.opts.Cast = transport
	w := req(a, admin, "POST", discover, "{}") // no actual direct LAN socket
	want(t, w, 403)
	if !strings.Contains(w.Body.String(), "cast_lan_only") {
		t.Fatal("missing LAN-only error")
	}
	lan := casting.LAN{Interface: net.Interface{Index: 3}, Addr: netip.MustParseAddr("192.168.20.10"), Prefix: netip.MustParsePrefix("192.168.20.0/24"), Port: 8080}
	a.castLAN = func(*http.Request) (casting.LAN, bool) { return lan, true }
	want(t, req(a, nil, "POST", discover, "{}"), 401)
	r := httptest.NewRequest("POST", discover, strings.NewReader("{}"))
	r.AddCookie(admin)
	r.Header.Set("Content-Type", "application/json")
	withoutHeader := httptest.NewRecorder()
	a.ServeHTTP(withoutHeader, r)
	want(t, withoutHeader, 403)
	want(t, req(a, admin, "PUT", "/api/admin/nas", `{"host":"nas.local","share":"films","username":"player","password":"`+nasPW+`"}`), 200)
	want(t, req(a, admin, "POST", "/api/admin/roots", `{"label":"Film","path":" movies"}`), 201)
	want(t, req(a, admin, "POST", "/api/admin/users", `{"username":"viewer","role":"viewer","password":"`+loginPW+`"}`), 201)
	viewer := signIn(t, a, "viewer")
	second := signIn(t, a, "viewer") // same account, distinct session
	w = req(a, viewer, "POST", discover, "{}")
	want(t, w, 200)
	var found struct{ Devices []publicDevice }
	if err := json.Unmarshal(w.Body.Bytes(), &found); err != nil || len(found.Devices) != 3 || found.Devices[deviceIndex].Name != "TV" || found.Devices[deviceIndex].Protocol != protocol || found.Devices[2].Name != "other endpoint" || strings.Contains(w.Body.String(), tv) || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("discovery data or CORS boundary")
	}
	id := found.Devices[deviceIndex].ID
	if found.Devices[0].ID == found.Devices[1].ID || found.Devices[1].ID == found.Devices[2].ID {
		t.Fatal("different endpoints shared an ID")
	}
	payload := `{"deviceId":"` + id + `","path":"` + file + `","startSeconds":1.25}`
	want(t, req(a, viewer, "POST", start, payload), 404)
	grant := func(value string) {
		t.Helper()
		want(t, req(a, admin, "PUT", "/api/admin/users/2/grants", `{"rootIds":`+value+`}`), 200)
	}
	grant("[1]")
	want(t, req(a, second, "POST", start, payload), 404)
	want(t, req(a, viewer, "POST", start, strings.Replace(payload, id, tv, 1)), 404)
	want(t, req(a, viewer, "POST", start, strings.Replace(payload, "1.25", `1.25,"protocol":"dlna"`, 1)), 400)
	want(t, req(a, viewer, "POST", start, strings.Replace(payload, file, "a.mkv", 1)), 415)
	want(t, req(a, viewer, "POST", start, strings.Replace(payload, "1.25", "-1", 1)), 400)
	want(t, req(a, viewer, "POST", start, strings.Replace(payload, "1.25", "0,\"url\":\"http://example.com\"", 1)), 400)
	var mediaPath, loadedURL string
	fetch := func(method, peer, target string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		ctx, cancel := context.WithTimeout(r.Context(), time.Second*3)
		defer cancel()
		r.RemoteAddr = net.JoinHostPort(peer, "51000")
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r.WithContext(ctx))
		return w
	}
	deny := func(w *httptest.ResponseRecorder) {
		t.Helper()
		want(t, w, 404)
		if w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
			t.Fatal("CORS on denial")
		}
		if w.Header().Get("transferMode.dlna.org") != "" || w.Header().Get("contentFeatures.dlna.org") != "" {
			t.Fatal("DLNA headers on denial")
		}
	}
	transport.load = func(d casting.Device, m casting.Media) error {
		prefix := "http://192.168.20.10:8080"
		if d.Kind() != protocol || d.Addr.String() != tv || m.Title != "a.M4V" || m.ContentType != "video/mp4" || m.StartSeconds != 1.25 || !strings.HasPrefix(m.URL, prefix+castMediaPath) {
			t.Fatal("unexpected media load")
		}
		loadedURL = m.URL
		mediaPath = strings.TrimPrefix(m.URL, prefix)
		// A synchronous receiver request would time out if LOAD held SQLite's connection.
		w := fetch("GET", tv, mediaPath, map[string]string{"Range": "bytes=2-3", "GetContentFeatures.dlna.org": "1"})
		want(t, w, 206)
		mode, features := "", ""
		if protocol == casting.ProtocolDLNA {
			mode, features = "Streaming", "DLNA.ORG_OP=01;DLNA.ORG_CI=0"
		}
		if w.Header().Get("transferMode.dlna.org") != mode || w.Header().Get("contentFeatures.dlna.org") != features || w.Header().Get("Content-Type") != "video/mp4" {
			t.Fatal("wrong range response metadata for protocol")
		}
		if w.Body.String() != "cd" || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Content-Range") != "bytes 2-3/8" || w.Header().Get("Cross-Origin-Resource-Policy") != "cross-origin" {
			t.Fatal("unexpected range response")
		}
		return nil
	}
	load := func() string {
		t.Helper()
		w := req(a, viewer, "POST", start, payload)
		want(t, w, 200)
		var result struct {
			Cast struct{ ID, DeviceID, DeviceName string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Cast.DeviceID != id || result.Cast.DeviceName != "TV" || result.Cast.ID == "" || strings.Contains(w.Body.String(), strings.TrimPrefix(mediaPath, castMediaPath)) {
			t.Fatal("control response exposed media or wrong contract")
		}
		return result.Cast.ID
	}
	controlID := load()
	w = fetch("HEAD", tv, mediaPath, nil)
	want(t, w, 200)
	if w.Header().Get("contentFeatures.dlna.org") != "" || (w.Header().Get("transferMode.dlna.org") == "Streaming") != (protocol == casting.ProtocolDLNA) {
		t.Fatal("unexpected HEAD metadata")
	}
	deny(fetch("GET", "192.168.20.43", mediaPath, map[string]string{"GetContentFeatures.dlna.org": "1"}))
	deny(fetch("GET", tv, mediaPath+"?path=another.mp4", nil))
	deny(fetch("GET", tv, castMediaPath+controlID, nil))
	lan.Port++
	deny(fetch("GET", tv, mediaPath, nil))
	lan.Port--
	preflight := map[string]string{"Origin": "https://receiver.example", "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "range", "Access-Control-Request-Private-Network": "true"}
	w = fetch("OPTIONS", tv, mediaPath, preflight)
	want(t, w, 204)
	if w.Header().Get("Access-Control-Allow-Private-Network") != "true" || w.Header().Get("Access-Control-Allow-Credentials") != "" || w.Header().Get("Access-Control-Allow-Headers") != "Range" {
		t.Fatal("preflight policy")
	}
	preflight["Access-Control-Request-Method"] = "PUT"
	w = fetch("OPTIONS", tv, mediaPath, preflight)
	want(t, w, 400)
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("allowed PUT preflight")
	}
	want(t, fetch("PUT", tv, mediaPath, nil), 405)
	want(t, req(a, second, "POST", stop, `{"castId":"`+controlID+`"}`), 404)
	grant("[]")
	deny(fetch("GET", tv, mediaPath, map[string]string{"Range": "bytes=2-"}))
	deny(fetch("OPTIONS", tv, mediaPath, nil))
	grant("[1]")
	load()
	priorPath := mediaPath
	controlID = load()
	deny(fetch("GET", tv, priorPath, nil))
	stopCalls := 0
	transport.stop = func(d casting.Device, url string) error {
		if d.Kind() != protocol || d.Addr.String() != tv || url != loadedURL {
			t.Fatal("stop not scoped to current media")
		}
		deny(fetch("GET", tv, mediaPath, nil))
		stopCalls++
		if stopCalls == 1 {
			want(t, req(a, viewer, "POST", stop, `{"castId":"`+controlID+`"}`), 409)
			return errors.New("receiver offline")
		}
		return nil
	}
	want(t, req(a, viewer, "POST", stop, `{"castId":"`+controlID+`"}`), 502)
	deny(fetch("GET", tv, mediaPath, map[string]string{"Range": "bytes=1-2"}))
	want(t, req(a, second, "POST", stop, `{"castId":"`+controlID+`"}`), 404)
	want(t, req(a, viewer, "POST", stop, `{"castId":"`+controlID+`"}`), 204)
	want(t, req(a, viewer, "POST", stop, `{"castId":"`+controlID+`"}`), 404)
	if stopCalls != 2 {
		t.Fatal("unexpected stop attempts")
	}
	deny(fetch("GET", tv, mediaPath, nil))
	load()
	want(t, req(a, viewer, "POST", "/api/logout", "{}"), 204)
	if len(a.casts.tickets) != 0 {
		t.Fatal("logout did not reclaim quota")
	}
	deny(fetch("HEAD", tv, mediaPath, nil))
	// A failed LOAD must revoke the ticket even if the receiver has already seen its URL.
	viewer = second
	w = req(a, viewer, "POST", discover, "{}")
	want(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &found); err != nil {
		t.Fatal(err)
	}
	payload = strings.Replace(payload, id, found.Devices[deviceIndex].ID, 1)
	id = found.Devices[deviceIndex].ID
	workingLoad := transport.load
	transport.load = func(_ casting.Device, m casting.Media) error {
		mediaPath = strings.TrimPrefix(m.URL, "http://"+lan.HTTPAddress())
		return errors.New(m.URL)
	}
	w = req(a, viewer, "POST", start, payload)
	want(t, w, 502)
	label := "Google Cast"
	if protocol == casting.ProtocolDLNA {
		label = "DLNA"
	}
	if strings.Contains(w.Body.String(), "/api/cast/media/") || strings.Contains(w.Body.String(), lan.HTTPAddress()) || !strings.Contains(w.Body.String(), label) {
		t.Fatal("transport error leaked ticket")
	}
	deny(fetch("HEAD", tv, mediaPath, nil))
	transport.load = workingLoad
	load()
	// External session invalidation is detected on the next receiver request.
	if _, err := a.db.Exec("DELETE FROM sessions WHERE hash=?", digest(viewer.Value)); err != nil {
		t.Fatal(err)
	}
	deny(fetch("GET", tv, mediaPath, nil))
	if len(a.casts.tickets) != 0 {
		t.Fatal("dead session retained ticket quota")
	}
}
