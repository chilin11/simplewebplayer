package dlnanet

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"simplewebplayer/internal/casting"
)

const envelopeNS = "http://schemas.xmlsoap.org/soap/envelope/"

var (
	errTransition      = errors.New("dlna: action unavailable in current transport state")
	errSeekUnavailable = errors.New("dlna: receiver cannot seek to the requested position")
)

const pollInterval = 270 * time.Millisecond

type session struct {
	endpoint
	client *http.Client
}
type argument struct{ name, value string }
type actionReply struct {
	XMLName   xml.Name
	URI       []string `xml:"CurrentURI"`
	State     []string `xml:"CurrentTransportState"`
	Status    []string `xml:"CurrentTransportStatus"`
	FaultCode string   `xml:"detail>UPnPError>errorCode"`
}
type envelope struct {
	XMLName xml.Name `xml:"http://schemas.xmlsoap.org/soap/envelope/ Envelope"`
	Bodies  []struct {
		Replies []actionReply `xml:",any"`
	} `xml:"http://schemas.xmlsoap.org/soap/envelope/ Body"`
}

func escaped(text string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(text))
	return b.String()
}

// action names and argument names are constants at the call sites, not device
// XML or browser input. Escape the DIDL URI/title inside DIDL, then escape the
// complete DIDL document once more as the SOAP argument text.
func (s *session) action(ctx context.Context, name string, args ...argument) (actionReply, error) {
	var empty actionReply
	ns := transportURN + strconv.Itoa(s.version)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="` + envelopeNS + `" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:` + name + ` xmlns:u="` + ns + `"><InstanceID>0</InstanceID>`)
	for _, a := range args {
		b.WriteString("<" + a.name + ">" + escaped(a.value) + "</" + a.name + ">")
	}
	b.WriteString("</u:" + name + "></s:Body></s:Envelope>")
	req, err := http.NewRequest(http.MethodPost, s.control, strings.NewReader(b.String()))
	if err != nil {
		return empty, errTarget
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+ns+"#"+name+`"`)
	data, err := request(ctx, s.client, req, 128<<10)
	if err != nil && len(data) == 0 {
		return empty, err
	}
	var env envelope
	if !validXML(data) || xml.Unmarshal(data, &env) != nil || len(env.Bodies) != 1 || len(env.Bodies[0].Replies) != 1 {
		return empty, errReceiver
	}
	r := env.Bodies[0].Replies[0]
	if r.XMLName == (xml.Name{Space: envelopeNS, Local: "Fault"}) {
		switch strings.TrimSpace(r.FaultCode) {
		case "701":
			return empty, errTransition
		case "401", "602", "710", "711": // Invalid/optional action, unsupported mode or illegal seek target.
			if name == "Seek" {
				return empty, errSeekUnavailable
			}
		}
		return empty, errAction
	}
	if err != nil {
		return empty, err
	}
	if r.XMLName.Space != ns || r.XMLName.Local != name+"Response" {
		return empty, errReceiver
	}
	return r, nil
}

func metadata(m casting.Media) string {
	title := label(m.Title)
	if title == "" {
		title = "Video"
	}
	return `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/"><item id="0" parentID="-1" restricted="1"><dc:title>` + escaped(title) + `</dc:title><upnp:class>object.item.videoItem</upnp:class><res protocolInfo="http-get:*:` + m.ContentType + `:*">` + escaped(m.URL) + `</res></item></DIDL-Lite>`
}

func (s *session) currentURI(ctx context.Context) (string, error) {
	r, err := s.action(ctx, "GetMediaInfo")
	if err != nil {
		return "", err
	}
	if len(r.URI) != 1 {
		return "", errReceiver
	}
	if r.URI[0] != "" {
		u, e := url.Parse(r.URI[0])
		if e != nil || !u.IsAbs() {
			return "", errReceiver
		}
	}
	return r.URI[0], nil
}
func (s *session) state(ctx context.Context) (string, error) {
	r, err := s.action(ctx, "GetTransportInfo")
	if err != nil {
		return "", err
	}
	if len(r.State) != 1 || len(r.Status) > 1 {
		return "", errReceiver
	}
	if len(r.Status) == 1 && r.Status[0] != "OK" && r.Status[0] != "NOT_IMPLEMENTED" {
		return "", errPlayback
	}
	return r.State[0], nil
}
func wait(ctx context.Context, delay time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

// The caller's Load deadline covers discovery, actions and buffering; in
// particular a slow post-Seek transition gets all of the remaining budget.
// A Play ACK disables further writes. Only an explicit 701 can enable retries.
func (s *session) waitPlaying(ctx context.Context, uri string, playRetries int) error {
	for {
		state, err := s.state(ctx)
		if err != nil {
			return err
		}
		delay := pollInterval
		switch state {
		case "PLAYING", "STOPPED", "PAUSED_PLAYBACK":
			if state != "PLAYING" && playRetries == 0 {
				break
			}
			// AVTransport has no compare-and-play. Re-read the URI immediately
			// before each retry; another source is never a reason to issue Play.
			current, err := s.currentURI(ctx)
			if err != nil {
				return err
			}
			if current != "" && current != uri {
				return errPlayback
			}
			if current == uri {
				if state == "PLAYING" {
					return nil
				}
				playRetries--
				_, err = s.action(ctx, "Play", argument{"Speed", "1"})
				switch {
				case err == nil:
					playRetries = 0
				case errors.Is(err, errTransition):
					delay = time.Second
				default:
					return err
				}
			}
		case "TRANSITIONING", "NO_MEDIA_PRESENT": // Not ready: read-only poll.
		default:
			return errPlayback
		}
		if err := wait(ctx, delay); err != nil {
			return err
		}
	}
}
func (s *session) load(ctx context.Context, m casting.Media) error {
	if _, err := s.action(ctx, "SetAVTransportURI", argument{"CurrentURI", m.URL}, argument{"CurrentURIMetaData", metadata(m)}); err != nil {
		return err
	}
	_, err := s.action(ctx, "Play", argument{"Speed", "1"})
	retries := 0
	if errors.Is(err, errTransition) {
		// Either auto-started or still preparing after SetURI. Observe first.
		retries = 2
	} else if err != nil {
		return err
	}
	if err := s.waitPlaying(ctx, m.URL, retries); err != nil {
		return err
	}
	if m.StartSeconds >= 1 && m.StartSeconds <= math.MaxInt32 {
		n := int64(m.StartSeconds)
		target := fmt.Sprintf("%02d:%02d:%02d", n/3600, (n/60)%60, n%60)
		// No Seek prior to confirmation of our own URI. Unsupported REL_TIME
		// is nonfatal; report playback only, never claim a confirmed position.
		_, err := s.action(ctx, "Seek", argument{"Unit", "REL_TIME"}, argument{"Target", target})
		if errors.Is(err, errSeekUnavailable) {
			// The receiver definitely rejected Seek; the matching PLAYING
			// observation immediately before it still establishes playback.
			return ctx.Err()
		}
		// Acknowledged or uncertain (e.g. lost HTTP response): never repeat
		// Seek and never infer either playback or the actual position from it.
		return s.waitPlaying(ctx, m.URL, 0)
	}
	return nil
}
func (s *session) stop(ctx context.Context, uri string) error {
	current, err := s.currentURI(ctx)
	if err != nil {
		return err
	}
	if current == "" {
		state, err := s.state(ctx)
		if err != nil {
			return err
		}
		if state == "STOPPED" || state == "NO_MEDIA_PRESENT" {
			return nil
		}
		return errReceiver
	}
	if current != uri {
		return nil
	}
	// AVTransport offers no compare-and-stop; query immediately beforehand
	// and never retry Stop against a possibly replaced URI on a fault.
	_, err = s.action(ctx, "Stop")
	if err != nil && !errors.Is(err, errTransition) {
		return err
	}
	// StopResponse is an ACK, not a stopped state. A 701 can also mean the
	// device stopped on its own. Reconcile by reading only, up to the shared
	// Stop deadline; an inconclusive result must preserve the caller's retry.
	for {
		state, err := s.state(ctx)
		if err != nil {
			return err
		}
		switch state {
		case "STOPPED", "NO_MEDIA_PRESENT":
			return nil
		case "TRANSITIONING", "PLAYING", "PAUSED_PLAYBACK", "RECORDING", "PAUSED_RECORDING":
			current, err := s.currentURI(ctx)
			if err != nil {
				return err
			}
			if current != "" && current != uri {
				return nil
			}
		default:
			return errReceiver
		}
		if err := wait(ctx, pollInterval); err != nil {
			return err
		}
	}
}

func localMedia(l casting.LAN, raw string) bool {
	if l.Port == 0 || len(raw) > 8192 || strings.ContainsAny(raw, "#\\ \r\n\t") || !utf8.ValidString(raw) {
		return false
	}
	u, err := url.ParseRequestURI(raw)
	return err == nil && u.Scheme == "http" && u.User == nil && u.Host == l.HTTPAddress() && len(u.Path) > 1 && strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//") && !strings.ContainsRune(u.Path, '\\')
}
func (t *Transport) Load(ctx context.Context, l casting.LAN, d casting.Device, m casting.Media) error {
	if !localMedia(l, m.URL) || (m.ContentType != "video/mp4" && m.ContentType != "video/webm") || len(m.Title) > 4096 || !utf8.ValidString(m.Title) || math.IsNaN(m.StartSeconds) || math.IsInf(m.StartSeconds, 0) || m.StartSeconds < 0 {
		return errMedia
	}
	ctx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	e, err := t.resolve(ctx, l, d, t.Discover)
	if err != nil {
		return err
	}
	s := session{e, clientFor(l, e.origin)}
	return s.load(ctx, m)
}
func (t *Transport) Stop(ctx context.Context, l casting.LAN, d casting.Device, uri string) error {
	if !localMedia(l, uri) {
		return errMedia
	}
	ctx, cancel := context.WithTimeout(ctx, 14*time.Second)
	defer cancel()
	e, err := t.resolve(ctx, l, d, t.Discover)
	if err != nil {
		return err
	}
	s := session{e, clientFor(l, e.origin)}
	return s.stop(ctx, uri)
}
