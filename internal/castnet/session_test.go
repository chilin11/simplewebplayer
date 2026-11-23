package castnet

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"simplewebplayer/internal/casting"
)

const testURL = "http://192.168.42.1:8080/cast/opaque-test-token"
const appRoute = "transport-42"

var appStatus = body{"applications": []body{{"appId": defaultApp, "sessionId": "session-23", "transportId": appRoute}}}

func statusURL(u string, state string) []body {
	return []body{{"mediaSessionId": 17, "playerState": state, "media": body{"contentId": u}}}
}

// Drive a real self-signed TLS handshake on an in-memory connection; never
// open a listening socket or send a command to the developer's network.
func TestTLSLoadSequence(t *testing.T) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, &cert, &cert, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer b.Close()
	server := tls.Server(b, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: k}}})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	b.SetDeadline(deadline)
	done := make(chan error, 1)
	go func() {
		done <- runChannel(ctx, a, func(s *session) error {
			return s.load(casting.Media{URL: testURL, Title: "客厅", ContentType: "video/mp4", StartSeconds: 12.5})
		})
	}()
	var sender string
	read := func(to, ns, typ string) (int, body) {
		t.Helper()
		m, err := readFrame(server)
		if err != nil {
			t.Fatal("reading command:", err)
		}
		if sender == "" {
			sender = m.source
		}
		var x body
		if json.Unmarshal(m.data, &x) != nil || m.source != sender || m.dest != to || m.ns != ns || x["type"] != typ {
			t.Fatalf("unexpected route or type, want %s", typ)
		}
		id, _ := x["requestId"].(float64)
		return int(id), x
	}
	write := func(src, to, ns string, x body) {
		t.Helper()
		data, err := json.Marshal(x)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFrame(server, envelope{source: src, dest: to, ns: ns, data: data}); err != nil {
			t.Fatal(err)
		}
	}
	read(receiver, nsConn, "CONNECT")
	read(receiver, nsHeartbeat, "PING")
	id, _ := read(receiver, nsReceiver, "GET_STATUS")
	if id < 1 {
		t.Fatal("missing request ID")
	}
	write(receiver, "*", nsHeartbeat, body{"type": "PING"})
	read(receiver, nsHeartbeat, "PONG")
	// Neither stale, unsolicited, misaddressed, nor wrong-namespace replies
	// may confirm this request, even when their requestId matches.
	for _, bad := range []struct {
		src, to, ns string
		id          int
	}{{"other", sender, nsReceiver, id}, {receiver, sender, nsReceiver, id + 8}, {receiver, "*", nsReceiver, id}, {receiver, "other", nsReceiver, id}, {receiver, sender, nsMedia, id}} {
		write(bad.src, bad.to, bad.ns, body{"type": "INVALID_REQUEST", "requestId": bad.id})
	}
	write(receiver, sender, nsReceiver, body{"type": "RECEIVER_STATUS", "requestId": id, "status": body{}})
	id, cmd := read(receiver, nsReceiver, "LAUNCH")
	if cmd["appId"] != defaultApp {
		t.Fatal("wrong application")
	}
	write(receiver, sender, nsReceiver, body{"type": "RECEIVER_STATUS", "requestId": id, "status": appStatus})
	read(appRoute, nsConn, "CONNECT")
	id, cmd = read(appRoute, nsMedia, "LOAD")
	m := cmd["media"].(map[string]any)
	if cmd["autoplay"] != true || cmd["currentTime"] != 12.5 || cmd["sessionId"] != "session-23" || m["contentId"] != testURL || m["contentType"] != "video/mp4" || m["metadata"].(map[string]any)["title"] != "客厅" {
		t.Fatal("wrong LOAD content")
	}
	select {
	case <-done:
		t.Fatal("reported success before acknowledgement")
	default:
	}
	write(appRoute, sender, nsMedia, body{"type": "MEDIA_STATUS", "requestId": id, "status": statusURL(testURL, "BUFFERING")})
	// A different endpoint, another request or another media session may
	// not confirm this load. Nor may a content-less delta after a queue
	// reported different content on the same media session ID.
	write("other-app", "*", nsMedia, body{"type": "MEDIA_STATUS", "requestId": 0, "status": statusURL(testURL, "PLAYING")})
	write(appRoute, sender, nsMedia, body{"type": "LOAD_FAILED", "requestId": id + 9})
	other := statusURL(testURL, "PLAYING")
	other[0]["mediaSessionId"] = 31
	write(appRoute, "*", nsMedia, body{"type": "MEDIA_STATUS", "requestId": 0, "status": other})
	write(appRoute, "*", nsMedia, body{"type": "MEDIA_STATUS", "requestId": 0, "status": statusURL(testURL+"-other", "PLAYING")})
	delta := []body{{"mediaSessionId": 17, "playerState": "PLAYING"}}
	write(appRoute, "*", nsMedia, body{"type": "MEDIA_STATUS", "requestId": 0, "status": delta})
	write(appRoute, "*", nsMedia, body{"type": "MEDIA_STATUS", "requestId": 0, "status": statusURL(testURL, "LOADING")})
	write(appRoute, "*", nsMedia, body{"type": "MEDIA_STATUS", "requestId": 0, "status": statusURL(testURL, "PAUSED")})
	write(receiver, "*", nsHeartbeat, body{"type": "PING"})
	read(receiver, nsHeartbeat, "PONG") // still waiting, with no premature detach
	write(appRoute, "*", nsMedia, body{"type": "MEDIA_STATUS", "requestId": 0, "status": delta})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := readFrame(server); err == nil {
		t.Fatal("sent a command after LOAD acknowledgement")
	}
}

type transcript struct {
	bytes.Reader
	out bytes.Buffer
}

func (t *transcript) Write(b []byte) (int, error) { return t.out.Write(b) }

func transcriptTest(t *testing.T, responses []body, op func(*session) error) ([]body, error) {
	t.Helper()
	var in bytes.Buffer
	for i, x := range responses {
		src, ns := appRoute, nsMedia
		if i == 0 {
			src, ns = receiver, nsReceiver
		}
		if _, set := x["requestId"]; !set {
			x["requestId"] = i + 1
		}
		data, err := json.Marshal(x)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFrame(&in, envelope{source: src, dest: "sender-test", ns: ns, data: data}); err != nil {
			t.Fatal(err)
		}
	}
	var rw transcript
	rw.Reset(in.Bytes())
	s := session{conn: &rw, sender: "sender-test"}
	err := op(&s)
	var cmds []body
	for rw.out.Len() > 0 {
		m, e := readFrame(&rw.out)
		if e != nil {
			t.Fatal(e)
		}
		var cmd body
		if json.Unmarshal(m.data, &cmd) != nil {
			t.Fatal("invalid outgoing JSON")
		}
		if cmd["type"] == "STOP" && (m.dest != appRoute || m.ns != nsMedia) {
			t.Fatal("STOP not scoped to app media channel")
		}
		cmds = append(cmds, cmd)
	}
	return cmds, err
}

func TestStopOnlyOwnMediaSession(t *testing.T) {
	for _, tc := range []struct {
		name       string
		app, items any
		want       bool
	}{{"other app", body{"applications": []body{{"appId": "other"}}}, nil, false}, {"idle device", body{}, nil, false}, {"idle media", appStatus, []body{}, false}, {"changed source", appStatus, statusURL(testURL+"-other", "PLAYING"), false}, {"matching media", appStatus, statusURL(testURL, "BUFFERING"), true}} {
		t.Run(tc.name, func(t *testing.T) {
			responses := []body{{"type": "RECEIVER_STATUS", "status": tc.app}}
			if tc.items != nil {
				responses = append(responses, body{"type": "MEDIA_STATUS", "status": tc.items})
			}
			if tc.want {
				responses = append(responses, body{"type": "MEDIA_STATUS", "status": []body{}})
			}
			cmds, err := transcriptTest(t, responses, func(s *session) error { return s.stop(testURL) })
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, cmd := range cmds {
				switch cmd["type"] {
				case "STOP":
					n++
					if cmd["mediaSessionId"] != float64(17) || cmd["sessionId"] != "session-23" {
						t.Fatal("STOP not scoped to observed media session")
					}
				case "LAUNCH", "LOAD":
					t.Fatal("STOP launched an application")
				}
			}
			if (n == 1) != tc.want || n > 1 {
				t.Fatal("unexpected STOP command count")
			}
		})
	}
}

func TestLoadRejectionsAndRedaction(t *testing.T) {
	for _, typ := range []string{"INVALID_REQUEST", "LOAD_FAILED"} {
		_, err := transcriptTest(t, []body{{"type": "RECEIVER_STATUS", "status": appStatus}, {"type": typ, "reason": testURL}}, func(s *session) error { return s.load(casting.Media{URL: testURL, ContentType: "video/mp4"}) })
		if !errors.Is(err, errRejected) || strings.Contains(err.Error(), "opaque-test-token") {
			t.Fatal("receiver rejection not safely surfaced")
		}
	}
}

func TestLoadProbeAndLateFailure(t *testing.T) {
	pending := body{"type": "MEDIA_STATUS", "status": []body{{"mediaSessionId": 17, "playerState": "BUFFERING"}}}
	start := []body{{"type": "RECEIVER_STATUS", "status": appStatus}, pending}
	idle := statusURL(testURL, "IDLE")
	idle[0]["idleReason"] = "ERROR"
	for _, tc := range []struct {
		name string
		last body
		want error
	}{
		{"probe confirms playing", body{"type": "MEDIA_STATUS", "requestId": 3, "status": statusURL(testURL, "PLAYING")}, nil},
		{"original LOAD fails after probe", body{"type": "LOAD_FAILED", "requestId": 2, "reason": testURL}, errRejected},
		{"async idle error", body{"type": "MEDIA_STATUS", "requestId": 0, "status": idle}, errRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responses := append(start, body{"type": "MEDIA_STATUS", "requestId": 0, "status": []body{{"mediaSessionId": 17, "playerState": "PLAYING"}}}, tc.last)
			cmds, err := transcriptTest(t, responses, func(s *session) error { return s.load(casting.Media{URL: testURL, ContentType: "video/mp4"}) })
			if !errors.Is(err, tc.want) {
				t.Fatalf("unexpected LOAD result: %v", err)
			}
			last := cmds[len(cmds)-1]
			if last["type"] != "GET_STATUS" || last["requestId"] != float64(3) || last["mediaSessionId"] != float64(17) {
				t.Fatal("missing scoped status probe")
			}
		})
	}
}

func TestLoadTimeoutWhileBuffering(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	a.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	b.SetWriteDeadline(time.Now().Add(time.Second))
	s := session{conn: a, sender: "sender-test", nextID: 2}
	done := make(chan error, 1)
	go func() { done <- s.waitPlaying(application{Transport: appRoute, Session: "session-23"}, 2, testURL) }()
	p, err := json.Marshal(body{"type": "MEDIA_STATUS", "requestId": 2, "status": statusURL(testURL, "BUFFERING")})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(b, envelope{source: appRoute, dest: s.sender, ns: nsMedia, data: p}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errChannel) {
			t.Fatalf("wanted socket deadline, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("load exceeded socket deadline")
	}
}

func TestCancelStalledTLS(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runChannel(ctx, a, func(*session) error { return nil }) }()
	var first [1]byte
	if _, err := b.Read(first[:]); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation not surfaced")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not release TLS socket")
	}
}
