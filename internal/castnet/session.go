package castnet

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"

	"simplewebplayer/internal/casting"
)

const (
	receiver         = "receiver-0"
	defaultApp       = "CC1AD845"
	nsConn           = "urn:x-cast:com.google.cast.tp.connection"
	nsHeartbeat      = "urn:x-cast:com.google.cast.tp.heartbeat"
	nsReceiver       = "urn:x-cast:com.google.cast.receiver"
	nsMedia          = "urn:x-cast:com.google.cast.media"
	maxSessionFrames = 192
)

var (
	errReply        = errors.New("cast: unexpected receiver status")
	errRejected     = errors.New("cast: receiver rejected command")
	errStaleSession = errors.New("cast: media session no longer current")
)

type session struct {
	conn           io.ReadWriter
	sender         string
	nextID, frames int
}

func newSession(c io.ReadWriter) *session {
	var id [8]byte
	rand.Read(id[:])
	return &session{conn: c, sender: "sender-" + hex.EncodeToString(id[:])}
}

type body map[string]any

func (s *session) send(to, ns string, b body) error {
	p, err := json.Marshal(b)
	if err != nil {
		return errReply
	}
	if writeFrame(s.conn, envelope{source: s.sender, dest: to, ns: ns, data: p}) != nil {
		return errChannel
	}
	return nil
}

type response struct {
	Type    string          `json:"type"`
	ID      int             `json:"requestId"`
	Status  json.RawMessage `json:"status"`
	MediaID int64           `json:"mediaSessionId"`
}

func (s *session) command(to, ns string, b body) (int, error) {
	s.nextID++
	b["requestId"] = s.nextID
	return s.nextID, s.send(to, ns, b)
}

func readChannel(r io.Reader) (envelope, error) {
	m, err := readFrame(r)
	if err != nil && !errors.Is(err, errFrame) {
		return envelope{}, errChannel
	}
	return m, err
}

// route handles channel housekeeping first. Only the expected endpoint and
// namespace may then contribute a response (directed or broadcast).
func (s *session) route(m envelope, to, ns string) (response, bool, error) {
	var res response
	if m.binary || m.dest != s.sender && m.dest != "*" || m.source != to && m.source != receiver {
		return res, false, nil
	}
	if m.ns != nsHeartbeat && m.ns != nsConn && (m.source != to || m.ns != ns) {
		return res, false, nil
	}
	if json.Unmarshal(m.data, &res) != nil {
		return res, false, errReply
	}
	switch m.ns {
	case nsHeartbeat:
		if res.Type == "PING" {
			return res, false, s.send(m.source, nsHeartbeat, body{"type": "PONG"})
		}
		return res, false, nil
	case nsConn:
		if res.Type == "CLOSE" {
			return res, false, errChannel
		}
		return res, false, nil
	}
	return res, true, nil
}

func (r response) failure() error {
	switch r.Type {
	case "INVALID_MEDIA_SESSION_ID":
		return errStaleSession
	case "LOAD_FAILED", "LOAD_CANCELLED", "LAUNCH_ERROR", "INVALID_REQUEST", "INVALID_PLAYER_STATE", "ERROR":
		return errRejected
	}
	return nil
}

func (s *session) request(to, ns, want string, b body) (json.RawMessage, error) {
	id, err := s.command(to, ns, b)
	if err != nil {
		return nil, err
	}
	for s.frames < maxSessionFrames {
		s.frames++
		m, err := readChannel(s.conn)
		if err != nil {
			return nil, err
		}
		res, ok, err := s.route(m, to, ns)
		if err != nil {
			return nil, err
		}
		if !ok || m.dest != s.sender || res.ID != id {
			continue
		}
		if res.Type == want {
			return res.Status, nil
		}
		if err := res.failure(); err != nil {
			return nil, err
		}
		return nil, errReply
	}
	return nil, errReply
}

type application struct {
	App       string `json:"appId"`
	Session   string `json:"sessionId"`
	Transport string `json:"transportId"`
}

func endpoint(s string) bool {
	return len(s) > 0 && len(s) <= 256 && s != "*" && s != receiver && strings.IndexFunc(s, unicode.IsControl) < 0
}

func (s *session) receiverApp(msg body) (application, error) {
	p, err := s.request(receiver, nsReceiver, "RECEIVER_STATUS", msg)
	if err != nil {
		return application{}, err
	}
	var status *struct {
		Apps []application `json:"applications"`
	}
	if json.Unmarshal(p, &status) != nil || status == nil {
		return application{}, errReply
	}
	var a application
	for _, v := range status.Apps {
		if v.App == defaultApp {
			if a.App != "" || !endpoint(v.Session) || !endpoint(v.Transport) || v.Transport == s.sender {
				return application{}, errReply
			}
			a = v
		}
	}
	return a, nil
}

func (s *session) attach(launch bool) (application, error) {
	if err := s.send(receiver, nsConn, body{"type": "CONNECT"}); err != nil {
		return application{}, err
	}
	if err := s.send(receiver, nsHeartbeat, body{"type": "PING"}); err != nil {
		return application{}, err
	}
	a, err := s.receiverApp(body{"type": "GET_STATUS"})
	if err != nil {
		return application{}, err
	}
	if a.App == "" && launch {
		a, err = s.receiverApp(body{"type": "LAUNCH", "appId": defaultApp})
		if err != nil {
			return application{}, err
		}
		if a.App == "" {
			a, err = s.receiverApp(body{"type": "GET_STATUS"})
			if err != nil {
				return application{}, err
			}
			if a.App == "" {
				return application{}, errReply
			}
		}
	}
	if a.App != "" {
		err = s.send(a.Transport, nsConn, body{"type": "CONNECT"})
	}
	return a, err
}

type mediaStatus struct {
	ID         int64  `json:"mediaSessionId"`
	State      string `json:"playerState"`
	IdleReason string `json:"idleReason"`
	Media      struct {
		URL string `json:"contentId"`
	} `json:"media"`
}

func (m mediaStatus) active() bool {
	return m.State == "PLAYING" || m.State == "BUFFERING" || m.State == "PAUSED" || m.State == "LOADING"
}

func (s *session) media(a application, b body) ([]mediaStatus, error) {
	b["sessionId"] = a.Session
	p, err := s.request(a.Transport, nsMedia, "MEDIA_STATUS", b)
	if err != nil {
		return nil, err
	}
	return parseMedia(p)
}

func parseMedia(p json.RawMessage) ([]mediaStatus, error) {
	var items []mediaStatus
	if json.Unmarshal(p, &items) != nil || items == nil {
		return nil, errReply
	}
	return items, nil
}

func (s *session) load(m casting.Media) error {
	a, err := s.attach(true)
	if err != nil {
		return err
	}
	id, err := s.command(a.Transport, nsMedia, body{"type": "LOAD", "sessionId": a.Session, "autoplay": true, "currentTime": m.StartSeconds, "media": body{"contentId": m.URL, "contentType": m.ContentType, "streamType": "BUFFERED", "metadata": body{"metadataType": 0, "title": m.Title}}})
	if err != nil {
		return err
	}
	return s.waitPlaying(a, id, m.URL)
}

type pendingLoad struct {
	url            string
	bound, hint    int64 // confirmed media session vs an unverified ID to query
	contentMatches bool
}

func (p *pendingLoad) observe(items []mediaStatus, loadReply bool) (bool, error) {
	for _, item := range items {
		// A direct failure may precede allocation of a mediaSessionId.
		if loadReply && len(items) == 1 && item.ID <= 0 && item.State == "IDLE" && item.IdleReason == "ERROR" && (item.Media.URL == "" || item.Media.URL == p.url) {
			return false, errRejected
		}
		if item.ID <= 0 || p.bound != 0 && item.ID != p.bound {
			continue
		}
		switch {
		case item.Media.URL == p.url:
			p.bound, p.contentMatches = item.ID, true
		case item.Media.URL != "":
			// A queue can reuse its mediaSessionId for different content.
			if item.ID == p.bound {
				p.contentMatches = false
			}
			continue
		case !p.contentMatches:
			// A directed delta alone is not evidence of the contentId.
			if loadReply && len(items) == 1 {
				p.hint = item.ID
				if item.State == "IDLE" && item.IdleReason == "ERROR" {
					return false, errRejected
				}
			}
			continue
		}
		switch item.State {
		case "PLAYING":
			return true, nil
		case "IDLE":
			switch item.IdleReason {
			case "ERROR", "CANCELLED", "INTERRUPTED", "FINISHED":
				return false, errRejected
			}
		}
	}
	return false, nil
}

func (s *session) waitPlaying(a application, loadID int, url string) error {
	// A single reader allows sparse status probes without interrupting a
	// partial TLS frame or extending the socket's original deadline. This is
	// the terminal command: runChannel closes the socket on every return.
	type result struct {
		m   envelope
		err error
	}
	incoming := make(chan result)
	stop := make(chan struct{})
	defer close(stop)
	go func(left int) {
		for range left {
			m, err := readChannel(s.conn)
			select {
			case incoming <- result{m, err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}(maxSessionFrames - s.frames)
	p := pendingLoad{url: url}
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	probe := func() error {
		if s.nextID-loadID >= 10 {
			return nil
		}
		b := body{"type": "GET_STATUS", "sessionId": a.Session}
		if p.bound != 0 {
			b["mediaSessionId"] = p.bound
		} else if p.hint != 0 {
			b["mediaSessionId"] = p.hint
		}
		_, err := s.command(a.Transport, nsMedia, b)
		return err
	}
	for s.frames < maxSessionFrames {
		select {
		case <-ticker.C:
			if err := probe(); err != nil {
				return err
			}
		case in := <-incoming:
			s.frames++
			if in.err != nil {
				return in.err
			}
			r, ok, err := s.route(in.m, a.Transport, nsMedia)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			direct := in.m.dest == s.sender
			loadReply := direct && r.ID == loadID
			// GET_STATUS uses separate IDs; a late rejection of LOAD is still terminal.
			if loadReply {
				if err := r.failure(); err != nil {
					return err
				}
			}
			if r.ID == 0 && p.contentMatches && r.MediaID == p.bound && (r.Type == "LOAD_FAILED" || r.Type == "LOAD_CANCELLED") {
				return errRejected
			}
			if r.Type != "MEDIA_STATUS" || r.ID != 0 && !(direct && r.ID >= loadID && r.ID <= s.nextID) {
				continue
			}
			items, err := parseMedia(r.Status)
			if err != nil {
				if loadReply {
					return err
				}
				continue
			}
			playing, err := p.observe(items, loadReply)
			if err != nil || playing {
				return err
			}
			if loadReply && p.bound == 0 && p.hint != 0 && s.nextID == loadID {
				if err := probe(); err != nil {
					return err
				}
			}
		}
	}
	return errReply
}

func (s *session) stop(mediaURL string) error {
	a, err := s.attach(false)
	if err != nil || a.App == "" {
		return err
	}
	items, err := s.media(a, body{"type": "GET_STATUS"})
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.State != "IDLE" && item.Media.URL == "" {
			return errReply
		} // Unknown is not a confirmed content change.
		if item.Media.URL != mediaURL || item.State == "IDLE" {
			continue
		}
		if item.ID <= 0 || !item.active() {
			return errReply
		}
		// STOP only this media session. Never send receiver STOP (which would
		// shut down the whole app), nor retry STOP against a replacement ID.
		after, err := s.media(a, body{"type": "STOP", "mediaSessionId": item.ID})
		if errors.Is(err, errStaleSession) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, v := range after {
			if v.ID == item.ID && v.State != "IDLE" && (v.Media.URL == "" || v.Media.URL == mediaURL) {
				return errReply
			}
		}
		return nil
	}
	return nil
}
