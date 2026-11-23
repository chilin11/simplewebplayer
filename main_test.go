package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"simplewebplayer/internal/castnet"
	"simplewebplayer/internal/nas"
	"simplewebplayer/internal/player"
)

// TestLinuxPlaybackFlow exercises the shipped router and an isolated, signed
// SMB fixture. It is opt-in so ordinary local tests start no external services.
func TestLinuxPlaybackFlow(t *testing.T) {
	addr, nasPassword := os.Getenv("PLAYER_TEST_SMB_ADDRESS"), os.Getenv("PLAYER_TEST_SMB_PASSWORD")
	if addr == "" || nasPassword == "" {
		t.Skip("isolated SMB fixture not configured")
	}
	host, rawPort, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	app, err := player.Open(t.TempDir(), player.Options{Media: nas.New()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	const adminPassword, viewerPassword = "admin-fixture-test", "viewer-fixture-test"
	if err := app.InitAdmin("fixture-admin", adminPassword); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(routes(app))
	defer server.Close()
	newClient := func() *http.Client { jar, _ := cookiejar.New(nil); return &http.Client{Jar: jar} }
	admin, viewer := newClient(), newClient()
	request := func(c *http.Client, method, path string, data any, byteRange string, want int) (http.Header, []byte) {
		t.Helper()
		var body io.Reader
		if data != nil {
			encoded, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			body = bytes.NewReader(encoded)
		}
		r, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if data != nil {
			r.Header.Set("X-Player-Request", "1")
			r.Header.Set("Content-Type", "application/json")
		}
		if byteRange != "" {
			r.Header.Set("Range", byteRange)
		}
		resp, err := c.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s %s: got %d want %d (%s)", method, path, resp.StatusCode, want, string(out))
		}
		return resp.Header, out
	}
	obj := func(kv ...any) map[string]any {
		m := make(map[string]any, len(kv)/2)
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	id := func(body []byte, field string) int {
		t.Helper()
		var decoded map[string]struct{ ID int }
		if err := json.Unmarshal(body, &decoded); err != nil || decoded[field].ID == 0 {
			t.Fatalf("response missing %s ID: %v", field, err)
		}
		return decoded[field].ID
	}

	headers, html := request(admin, "GET", "/", nil, "", 200)
	if !strings.Contains(headers.Get("Content-Security-Policy"), "script-src 'self'") || !bytes.Contains(html, []byte("/assets/app.js")) {
		t.Fatal("embedded frontend or CSP missing")
	}
	request(admin, "GET", "/assets/app.js", nil, "", 200)
	request(admin, "POST", "/api/login", obj("username", "fixture-admin", "password", adminPassword), "", 200)
	_, conf := request(admin, "PUT", "/api/admin/nas", obj("host", host, "port", port, "share", "media", "username", "smbfixture", "password", nasPassword), "", 200)
	if bytes.Contains(conf, []byte(nasPassword)) {
		t.Fatal("configuration returns the SMB secret")
	}
	request(admin, "POST", "/api/admin/nas/test", obj(), "", 200)
	_, root := request(admin, "POST", "/api/admin/roots", obj("label", "播放目录", "path", "allowed"), "", 201)
	_, hidden := request(admin, "POST", "/api/admin/roots", obj("label", "未授权", "path", "hidden"), "", 201)
	_, user := request(admin, "POST", "/api/admin/users", obj("username", "viewer", "password", viewerPassword, "role", "viewer"), "", 201)
	rootID := id(root, "root")
	grants := "/api/admin/users/" + strconv.Itoa(id(user, "user")) + "/grants"
	request(admin, "PUT", grants, obj("rootIds", []int{rootID}), "", 200)
	prefix := "/api/library/" + strconv.Itoa(rootID)
	request(viewer, "GET", prefix+"/browse?path=", nil, "", 401)
	request(viewer, "POST", "/api/login", obj("username", "viewer", "password", viewerPassword), "", 200)
	request(viewer, "GET", "/api/admin/users", nil, "", 403)
	request(viewer, "GET", "/api/library/"+strconv.Itoa(id(hidden, "root"))+"/stream?path=sample.mp4", nil, "", 404)
	_, listing := request(viewer, "GET", prefix+"/browse?path=", nil, "", 200)
	if !bytes.Contains(listing, []byte("sample.mp4")) || bytes.Contains(listing, []byte("symlink.mp4")) {
		t.Fatal("SMB listing omitted the video or exposed a link")
	}
	stream := prefix + "/stream?path=sample.mp4"
	h, data := request(viewer, "GET", stream, nil, "bytes=2-4", 206)
	if string(data) != "234" || h.Get("Content-Range") != "bytes 2-4/10" {
		t.Fatal("wrong bytes or Content-Range from SMB")
	}
	h, data = request(viewer, "HEAD", stream, nil, "", 200)
	if len(data) != 0 || h.Get("Content-Length") != "10" {
		t.Fatal("invalid media HEAD response")
	}
	download := prefix + "/download?path=sample.mp4"
	h, data = request(viewer, "GET", download, nil, "bytes=2-4", 206)
	kind, params, err := mime.ParseMediaType(h.Get("Content-Disposition"))
	if err != nil || kind != "attachment" || params["filename"] != "sample.mp4" || string(data) != "234" {
		t.Fatal("authorized download did not return the requested original bytes as an attachment")
	}
	h, _ = request(viewer, "HEAD", download, nil, "", 200)
	if h.Get("Content-Length") != "10" {
		t.Fatal("download HEAD did not advertise the original length")
	}
	request(admin, "PUT", grants, obj("rootIds", []int{}), "", 200)
	request(viewer, "GET", stream, nil, "bytes=0-1", 404)
	h, _ = request(viewer, "GET", download, nil, "bytes=0-1", 404)
	if h.Get("Content-Disposition") != "" {
		t.Fatal("withdrawn grant produced an attachment")
	}
}

// Exercise the actual application router with the production Cast transport;
// a loopback connection must never start discovery, even with a valid login.
func TestCastRouterRejectsLoopback(t *testing.T) {
	app, err := player.Open(t.TempDir(), player.Options{Media: nas.New(), Cast: castnet.New()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.InitAdmin("fixture-admin", "only-a-test-password"); err != nil {
		t.Fatal(err)
	}
	h := routes(app)
	post := func(path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "http://127.0.0.1:8080"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:31337"
		r = r.WithContext(context.WithValue(t.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Player-Request", "1")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	login := post("/api/login", `{"username":"fixture-admin","password":"only-a-test-password"}`, nil)
	cookies := login.Result().Cookies()
	if login.Code != 200 || len(cookies) != 1 {
		t.Fatal("test login failed")
	}
	w := post("/api/cast/discover", `{}`, cookies[0])
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "cast_lan_only") {
		t.Fatalf("Cast discovery not blocked on loopback: HTTP %d", w.Code)
	}
}

func TestInitAdminIfNeededSkipsExistingAdmin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PLAYER_DATA_DIR", dir)
	app, err := player.Open(dir, player.Options{})
	if err != nil {
		t.Fatal(err)
	}
	err = app.InitAdmin("existing-admin", "only-a-test-password")
	app.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"init-admin", "--if-needed"}); err != nil {
		t.Fatalf("upgrade should not require resetting existing credentials: %v", err)
	}
}
