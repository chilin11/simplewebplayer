package dlnanet

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"simplewebplayer/internal/casting"
)

const fakeURI = "http://192.168.24.1:8080/cast/test?key=opaque&v=1"
const fakeTitle = `客厅🎞 <&> "电影"`

type step struct {
	action string
	fields string
	fault  string
	delay  time.Duration
}

func fault(action, code string) step { return step{action: action, fault: code} }
func stateReply(state string) step {
	return step{action: "GetTransportInfo", fields: "<CurrentTransportState>" + state + "</CurrentTransportState><CurrentTransportStatus>OK</CurrentTransportStatus>"}
}
func current(uri string) step {
	return step{action: "GetMediaInfo", fields: "<CurrentURI>" + escaped(uri) + "</CurrentURI>"}
}

func fakeSession(t *testing.T, steps ...step) *session {
	t.Helper()
	q := make(chan step, len(steps))
	for _, s := range steps {
		q <- s
	}
	ns := transportURN + "2"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var next step
		select {
		case next = <-q:
		default:
			t.Error("unexpected extra TV command")
			http.Error(w, "unexpected", 400)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/AVTransport" || r.Header.Get("SOAPAction") != `"`+ns+"#"+next.action+`"` || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/xml") {
			t.Errorf("expected SOAP %s", next.action)
		}
		var msg struct {
			Bodies []struct {
				Commands []struct {
					XMLName    xml.Name
					InstanceID *string
					CurrentURI string
					Metadata   string `xml:"CurrentURIMetaData"`
					Speed      string
					Unit       string
					Target     string
				} `xml:",any"`
			} `xml:"http://schemas.xmlsoap.org/soap/envelope/ Body"`
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil || xml.Unmarshal(b, &msg) != nil || len(msg.Bodies) != 1 || len(msg.Bodies[0].Commands) != 1 {
			t.Error("malformed outgoing SOAP")
			http.Error(w, "bad XML", 400)
			return
		}
		cmd := msg.Bodies[0].Commands[0]
		if cmd.XMLName != (xml.Name{Space: ns, Local: next.action}) || cmd.InstanceID == nil || *cmd.InstanceID != "0" {
			t.Error("invalid SOAP action namespace or instance")
		}
		switch next.action {
		case "SetAVTransportURI":
			var didl struct {
				XMLName xml.Name `xml:"urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/ DIDL-Lite"`
				Item    struct {
					Title string `xml:"http://purl.org/dc/elements/1.1/ title"`
					Res   struct {
						Info     string  `xml:"protocolInfo,attr"`
						Duration *string `xml:"duration,attr"`
						URI      string  `xml:",chardata"`
					} `xml:"res"`
				} `xml:"item"`
			}
			if cmd.CurrentURI != fakeURI || xml.Unmarshal([]byte(cmd.Metadata), &didl) != nil || didl.Item.Title != fakeTitle || didl.Item.Res.URI != fakeURI || didl.Item.Res.Info != "http-get:*:video/mp4:*" || didl.Item.Res.Duration != nil {
				t.Error("DIDL did not round trip actual URI, Unicode title and MIME without invented duration")
			}
		case "Play":
			if cmd.Speed != "1" {
				t.Error("not normal speed")
			}
		case "Seek":
			if cmd.Unit != "REL_TIME" || cmd.Target != "00:01:23" {
				t.Error("incorrect seek")
			}
		}
		if next.delay > 0 {
			if err := wait(r.Context(), next.delay); err != nil {
				return
			}
		}
		body := `<u:` + next.action + `Response xmlns:u="` + ns + `">` + next.fields + `</u:` + next.action + `Response>`
		if next.fault != "" {
			w.WriteHeader(500)
			body = `<s:Fault><faultcode>s:Client</faultcode><faultstring>` + escaped(fakeURI) + `</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>` + next.fault + `</errorCode></UPnPError></detail></s:Fault>`
		}
		_, _ = io.WriteString(w, `<s:Envelope xmlns:s="`+envelopeNS+`"><s:Body>`+body+`</s:Body></s:Envelope>`)
	}))
	t.Cleanup(func() {
		srv.Close()
		if len(q) != 0 {
			t.Errorf("%d expected receiver actions not sent", len(q))
		}
	})
	c := loopCandidate(t, srv.URL+"/AVTransport")
	e := endpoint{origin: c.origin, control: c.location.String(), version: 2, until: c.until}
	return &session{e, clientFor(loopLAN, e.origin)}
}

func TestPlayAndConditionalStop(t *testing.T) {
	s := fakeSession(t, step{action: "SetAVTransportURI"}, step{action: "Play"}, stateReply("TRANSITIONING"), stateReply("PLAYING"), current(fakeURI), fault("Seek", "710"),
		// Seek explicitly rejected: no redundant confirmation of the prior Play.
		current("http://other.invalid/changed"), current(fakeURI), step{action: "Stop"},
		stateReply("TRANSITIONING"), current(fakeURI), stateReply("STOPPED"))
	m := casting.Media{URL: fakeURI, Title: fakeTitle, ContentType: "video/mp4", StartSeconds: 83.4}
	if err := s.load(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := s.stop(t.Context(), fakeURI); err != nil {
		t.Fatal("other owner", err)
	}
	if err := s.stop(t.Context(), fakeURI); err != nil {
		t.Fatal("our media", err)
	}
}

func TestFaultAndAmbiguousState(t *testing.T) {
	m := casting.Media{URL: fakeURI, Title: fakeTitle, ContentType: "video/mp4"}
	t.Run("auto-playing", func(t *testing.T) {
		s := fakeSession(t, step{action: "SetAVTransportURI"}, fault("Play", "701"), stateReply("PLAYING"), current(fakeURI))
		if err := s.load(t.Context(), m); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("fault-no-ticket", func(t *testing.T) {
		s := fakeSession(t, fault("SetAVTransportURI", "714"))
		if err := s.load(t.Context(), m); !errors.Is(err, errAction) || strings.Contains(err.Error(), "opaque") {
			t.Fatal("expected redacted SOAP failure")
		}
	})
	t.Run("wrong-URI-not-playing-our-ticket", func(t *testing.T) {
		s := fakeSession(t, stateReply("PLAYING"), current("http://other.invalid/changed"))
		if err := s.waitPlaying(t.Context(), fakeURI, 0); !errors.Is(err, errPlayback) {
			t.Error("accepted a playing state for unrelated media")
		}
	})
	t.Run("701-ready-and-still-ours", func(t *testing.T) {
		s := fakeSession(t, step{action: "SetAVTransportURI"}, fault("Play", "701"), stateReply("TRANSITIONING"),
			stateReply("STOPPED"), current(fakeURI), fault("Play", "701"),
			stateReply("STOPPED"), current(fakeURI), fault("Play", "701"),
			// Even though the device reports STOPPED again, no third retry:
			// the next observation confirms the TV finally started on its own.
			stateReply("STOPPED"), stateReply("PLAYING"), current(fakeURI))
		if err := s.load(t.Context(), m); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("701-replaced-before-ready", func(t *testing.T) {
		s := fakeSession(t, step{action: "SetAVTransportURI"}, fault("Play", "701"), stateReply("STOPPED"), current("http://other.invalid/changed"))
		if err := s.load(t.Context(), m); !errors.Is(err, errPlayback) {
			t.Fatal("played or accepted another source", err)
		}
	})
	t.Run("stop-ack-not-stopped", func(t *testing.T) {
		s := fakeSession(t, current(fakeURI), step{action: "Stop"}, stateReply("PLAYING"), current(fakeURI), fault("GetTransportInfo", "501"),
			// A new caller retry is safe: check URI again. Replacement after
			// the second Stop is success, never a reason to send a third Stop.
			current(fakeURI), step{action: "Stop"}, stateReply("PLAYING"), current("http://other.invalid/changed"))
		if err := s.stop(t.Context(), fakeURI); err == nil {
			t.Fatal("Stop ACK misreported as stopped")
		}
		if err := s.stop(t.Context(), fakeURI); err != nil {
			t.Fatal("retry did not confirm replacement", err)
		}
	})
	t.Run("uncertain-seek-slow-rebuffer", func(t *testing.T) {
		// An unknown Seek fault is not an explicit refusal of REL_TIME. Give
		// a rebuffer longer than six seconds the original remaining deadline.
		buffer := stateReply("TRANSITIONING")
		buffer.delay = 1400 * time.Millisecond
		s := fakeSession(t, step{action: "SetAVTransportURI"}, step{action: "Play"}, stateReply("PLAYING"), current(fakeURI), fault("Seek", "501"), buffer, buffer, buffer, buffer, stateReply("PLAYING"), current(fakeURI))
		ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
		defer cancel()
		resume := m
		resume.StartSeconds = 83.4
		if err := s.load(ctx, resume); err != nil {
			t.Fatal("premature post-seek timeout", err)
		}
	})
	t.Run("missing-currentURI", func(t *testing.T) {
		s := fakeSession(t, step{action: "GetMediaInfo"})
		if err := s.stop(t.Context(), fakeURI); !errors.Is(err, errReceiver) {
			t.Error("missing URI reported as stopped")
		}
	})
	t.Run("empty-while-playing", func(t *testing.T) {
		s := fakeSession(t, current(""), stateReply("PLAYING"))
		if err := s.stop(t.Context(), fakeURI); !errors.Is(err, errReceiver) {
			t.Error("unknown active session reported stopped")
		}
	})
}
