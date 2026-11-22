package player

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"simplewebplayer/internal/media"
)

const loginPW = "fixture-password-42"
const nasPW = "test-share-secret-42"

type fakeMedia struct {
	started chan struct{}
	release chan struct{}
	stream  func(http.ResponseWriter, *http.Request, string) error
}

func (f *fakeMedia) Test(_ context.Context, s media.Settings) error {
	if s.Password != nasPW {
		return errors.New("unexpected probe credentials")
	}
	return nil
}

func (f *fakeMedia) List(_ context.Context, s media.Settings, base, path string) ([]media.Entry, error) {
	if s.Password != nasPW || base != " movies" || path != " x%2fy" {
		return nil, errors.New("unexpected media arguments")
	}
	return nil, nil
}
func (f *fakeMedia) Stream(w http.ResponseWriter, r *http.Request, s media.Settings, base, path string) error {
	if s.Password != nasPW || base != " movies" {
		return errors.New("unexpected media arguments")
	}
	if f.stream != nil {
		return f.stream(w, r, path)
	}
	if path != "a.mp4" {
		return errors.New("unexpected media path")
	}
	close(f.started)
	select {
	case <-f.release:
	case <-r.Context().Done():
		return r.Context().Err()
	}
	w.WriteHeader(206)
	return errors.New("simulated post-header failure")
}
func req(a *App, c *http.Cookie, m, path, data string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(m, path, strings.NewReader(data))
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if c != nil {
		r.AddCookie(c)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Player-Request", "1")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func want(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status %d, want %d, response %s", w.Code, code, w.Body.String())
	}
}
func signIn(t *testing.T, a *App, name string) *http.Cookie {
	t.Helper()
	w := req(a, nil, "POST", "/api/login", `{"username":"`+name+`","password":"`+loginPW+`"}`)
	want(t, w, 200)
	cs := w.Result().Cookies()
	if len(cs) != 1 || !cs[0].HttpOnly || !cs[0].Secure || cs[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("missing session cookie protections")
	}
	return cs[0]
}
func setup(t *testing.T, dir string, m media.Backend) (*App, *http.Cookie) {
	t.Helper()
	a, e := Open(dir, Options{Media: m, SecureCookies: true})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	if e = a.InitAdmin("first", loginPW); e != nil {
		t.Fatal(e)
	}
	return a, signIn(t, a, "first")
}
func TestConcurrentLastAdmin(t *testing.T) {
	dir := t.TempDir()
	a, first := setup(t, dir, nil)
	want(t, req(a, first, "POST", "/api/admin/users", `{"username":"second","password":"`+loginPW+`","role":"admin"}`), 201)
	b, e := Open(dir, Options{SecureCookies: true})
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	second := signIn(t, b, "second")
	start := make(chan struct{})
	ch := make(chan int, 2)
	go func() { <-start; ch <- req(a, first, "PATCH", "/api/admin/users/1", `{"enabled":false}`).Code }()
	go func() { <-start; ch <- req(b, second, "PATCH", "/api/admin/users/2", `{"role":"viewer"}`).Code }()
	close(start)
	n, m := <-ch, <-ch
	if !(n == 200 && m == 409 || n == 409 && m == 200) {
		t.Fatalf("concurrent change statuses %d, %d", n, m)
	}
	n = req(a, first, "GET", "/api/admin/users", "").Code
	m = req(b, second, "GET", "/api/admin/users", "").Code
	if !(n == 200 && m == 401 || n == 401 && m == 200) {
		t.Fatalf("retained sessions %d, %d", n, m)
	}
}
func TestGrantsAndCredentialLifecycle(t *testing.T) {
	dir := t.TempDir()
	backend := &fakeMedia{started: make(chan struct{}), release: make(chan struct{})}
	a, admin := setup(t, dir, backend)
	want(t, req(a, admin, "POST", "/api/admin/users", `{"username":"viewer","role":"viewer","password":"`+loginPW+`"}`), 201)
	viewer := signIn(t, a, "viewer")
	nas := "/api/admin/nas"
	roots := "/api/admin/roots"
	grants := "/api/admin/users/2/grants"
	browse := "/api/library/1/browse?path="
	stream := "/api/library/1/stream?path=a.mp4"
	want(t, req(a, viewer, "GET", nas, ""), 403)
	w := req(a, admin, "PUT", nas, `{"host":"nas.local","share":"films","username":"player","password":"`+nasPW+`"}`)
	want(t, w, 200)
	if strings.Contains(w.Body.String(), nasPW) {
		t.Fatal("leaked NAS password")
	}
	want(t, req(a, admin, "POST", roots, `{"label":"Film","path":" movies"}`), 201)
	want(t, req(a, admin, "PUT", nas, `{"host":"nas.local","share":"other","username":"player"}`), 409)
	want(t, req(a, admin, "PUT", nas, `{"host":"nas.local","share":"films","username":"player"}`), 200)
	want(t, req(a, nil, "GET", browse+"%zz", ""), 401)
	want(t, req(a, viewer, "GET", browse+"%zz", ""), 404)
	want(t, req(a, admin, "PUT", grants, `{"rootIds":[1]}`), 200)
	want(t, req(a, admin, "PUT", grants, `{"rootIds":[999]}`), 400)
	want(t, req(a, viewer, "GET", browse+"%zz", ""), 400)
	w = req(a, viewer, "GET", browse+"%20x%252fy", "")
	want(t, w, 200)
	if !strings.Contains(w.Body.String(), `"entries":[]`) {
		t.Fatal("expected empty JSON list")
	}
	w = req(a, viewer, "GET", "/api/library", "")
	want(t, w, 200)
	if strings.Contains(w.Body.String(), "path") {
		t.Fatal("library exposed NAS path")
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- req(a, viewer, "HEAD", stream, "") }()
	select {
	case <-backend.started:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not start")
	}
	revoked := req(a, admin, "PUT", grants, `{"rootIds":[]}`)
	close(backend.release)
	w = <-done
	want(t, revoked, 200)
	want(t, w, 206)
	if w.Body.Len() != 0 {
		t.Fatal("appended JSON after stream started")
	}
	want(t, req(a, viewer, "HEAD", stream, ""), 404)
	want(t, req(a, admin, "PUT", grants, `{"rootIds":[1]}`), 200)
	var ct []byte
	if e := a.db.QueryRow("SELECT secret FROM nas").Scan(&ct); e != nil || bytes.Contains(ct, []byte(nasPW)) {
		t.Fatal("credential not sealed")
	}
	var hash []byte
	if e := a.db.QueryRow("SELECT hash FROM sessions WHERE user_id=2").Scan(&hash); e != nil || !bytes.Equal(hash, digest(viewer.Value)) {
		t.Fatal("unexpected persisted session token")
	}
	if e := a.Close(); e != nil {
		t.Fatal(e)
	}
	b, e := Open(dir, Options{Media: backend})
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	want(t, req(b, viewer, "GET", browse+"%20x%252fy", ""), 200)
	want(t, req(b, admin, "DELETE", roots+"/1", ""), 204)
	w = req(b, admin, "GET", grants, "")
	var g struct {
		IDs []int64 `json:"rootIds"`
	}
	if json.Unmarshal(w.Body.Bytes(), &g) != nil || g.IDs == nil || len(g.IDs) != 0 {
		t.Fatal("grants not cleared")
	}
	want(t, req(b, admin, "PATCH", "/api/admin/users/2", `{"password":"new-fixture-password"}`), 200)
	want(t, req(b, viewer, "GET", "/api/library", ""), 401)
	for _, name := range []string{"player.key", "player.db"} {
		st, e := os.Stat(filepath.Join(dir, name))
		if e != nil || st.Mode().Perm() != 0600 {
			t.Fatal("private file permissions")
		}
	}
	if e = b.Close(); e != nil {
		t.Fatal(e)
	}
	key := filepath.Join(dir, "player.key")
	if e = os.Remove(key); e != nil {
		t.Fatal(e)
	}
	if opened, e := Open(dir, Options{}); e == nil {
		opened.Close()
		t.Fatal("accepted missing instance key")
	}
	if _, e = os.Stat(key); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("silently regenerated missing key")
	}
}

func TestLibraryDownload(t *testing.T) {
	const (
		filename  = "电影 100%2f;片段.mp4"
		mediaPath = "nested/" + filename
		video     = "abcdef"
		grants    = "/api/admin/users/2/grants"
		download  = "/api/library/1/download?path="
		stream    = "/api/library/1/stream?path="
	)
	calls := 0
	backend := &fakeMedia{stream: func(w http.ResponseWriter, r *http.Request, path string) error {
		calls++
		if w.Header().Get("Content-Disposition") != "" {
			t.Error("attachment set before the backend committed a response")
		}
		switch path {
		case mediaPath:
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Cache-Control", "no-store")
			http.ServeContent(w, r, filename, time.Time{}, strings.NewReader(video))
			return errors.New("simulated post-header failure")
		case "missing.mp4":
			return &media.Error{Status: 404, Code: "not_found", Message: "missing video"}
		case "unwritten.mp4":
			return nil
		default:
			return errors.New("unexpected media path: " + path)
		}
	}}
	a, admin := setup(t, t.TempDir(), backend)
	want(t, req(a, admin, "POST", "/api/admin/users", `{"username":"viewer","role":"viewer","password":"`+loginPW+`"}`), 201)
	viewer := signIn(t, a, "viewer")
	want(t, req(a, admin, "PUT", "/api/admin/nas", `{"host":"nas.local","share":"films","username":"player","password":"`+nasPW+`"}`), 200)
	want(t, req(a, admin, "POST", "/api/admin/roots", `{"label":"Film","path":" movies"}`), 201)

	checkRejected := func(cookie *http.Cookie, target string, status int, code string, backendCalls int) {
		t.Helper()
		before := calls
		w := req(a, cookie, "GET", target, "")
		want(t, w, status)
		var result struct{ Error struct{ Code string } }
		if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Error.Code != code {
			t.Fatalf("%s: expected JSON error %q, got %s", target, code, w.Body.String())
		}
		if w.Result().Header.Get("Content-Disposition") != "" || calls-before != backendCalls {
			t.Fatalf("%s: unexpected attachment or backend call count: %d", target, calls-before)
		}
	}
	checkRejected(nil, download+"%zz", 401, "unauthorized", 0)
	checkRejected(viewer, download+"%zz", 404, "not_found", 0)
	want(t, req(a, admin, "PUT", grants, `{"rootIds":[1]}`), 200)
	checkRejected(viewer, download+"%zz", 400, "invalid_request", 0)
	checkRejected(viewer, download+"bad%0D%0Aname.mp4", 400, "invalid_path", 0)
	checkRejected(viewer, download+"missing.mp4", 404, "not_found", 1)

	query := url.QueryEscape(mediaPath)
	for _, tc := range []struct {
		name, method, target, byteRange string
		status                          int
		body, contentRange              string
		attachment                      bool
	}{
		{"get", "GET", download + query, "", 200, video, "", true},
		{"head", "HEAD", download + query, "", 200, "", "", true},
		{"range", "GET", download + query, "bytes=2-4", 206, "cde", "bytes 2-4/6", true},
		{"unsatisfiable", "GET", download + query, "bytes=7-", 416, "", "bytes */6", false},
		{"playback", "GET", stream + query, "", 200, video, "", false},
		{"unwritten", "GET", download + "unwritten.mp4", "", 200, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.target, nil)
			r.AddCookie(viewer)
			if tc.byteRange != "" {
				r.Header.Set("Range", tc.byteRange)
			}
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			want(t, w, tc.status)
			h := w.Result().Header
			if got := h.Get("Content-Range"); got != tc.contentRange {
				t.Fatalf("Content-Range = %q, want %q", got, tc.contentRange)
			}
			if tc.status != http.StatusRequestedRangeNotSatisfiable && w.Body.String() != tc.body {
				t.Fatalf("body = %q, want %q", w.Body.String(), tc.body)
			}
			disposition := h.Get("Content-Disposition")
			if tc.attachment {
				kind, params, err := mime.ParseMediaType(disposition)
				if err != nil || kind != "attachment" || params["filename"] != filename || !strings.Contains(strings.ToLower(disposition), "filename*=utf-8''") {
					t.Fatalf("invalid attachment filename: %q", disposition)
				}
				if h.Get("Cache-Control") != "no-store" || h.Get("Content-Type") != "video/mp4" {
					t.Fatalf("unexpected media headers: %v", h)
				}
			} else if disposition != "" {
				t.Fatalf("unexpected attachment: %q", disposition)
			}
			if tc.method == "HEAD" && h.Get("Content-Length") != "6" {
				t.Fatalf("HEAD Content-Length = %q, want 6", h.Get("Content-Length"))
			}
		})
	}
	want(t, req(a, admin, "PUT", grants, `{"rootIds":[]}`), 200)
	checkRejected(viewer, download+query, 404, "not_found", 0)
}
