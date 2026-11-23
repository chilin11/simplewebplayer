package player

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"simplewebplayer/internal/media"
)

type fakeShareBrowser struct {
	fakeMedia
	discover func(context.Context, media.Settings) ([]string, error)
}

func (f *fakeShareBrowser) DiscoverShares(ctx context.Context, s media.Settings) ([]string, error) {
	return f.discover(ctx, s)
}

func TestPreviewShares(t *testing.T) {
	const nas = "/api/admin/nas"
	const path = nas + "/shares"
	const identity = `"host":"nas.local","username":"player"`
	const withPassword = `{` + identity + `,"password":"` + nasPW + `"}`
	f := &fakeShareBrowser{}
	a, admin := setup(t, t.TempDir(), f)
	want(t, req(a, admin, "POST", "/api/admin/users", `{"username":"viewer","role":"viewer","password":"`+loginPW+`"}`), 201)
	viewer := signIn(t, a, "viewer")
	want(t, req(a, viewer, "POST", path, withPassword), 403)
	want(t, req(a, nil, "POST", path, withPassword), 401)
	r := httptest.NewRequest("POST", path, strings.NewReader(withPassword))
	r.AddCookie(admin)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	want(t, w, 403)
	r.Header.Set("X-Player-Request", "1")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	want(t, w, 415)

	for _, extra := range []string{"", `,"share":"video"`, `,"password":""`, `,"password":"a\u0000b"`, `,"password":"` + strings.Repeat("界", 1025) + `"`, `,"password":123`} {
		want(t, req(a, admin, "POST", path, `{`+identity+extra+`}`), 400)
	}
	var seen []media.Settings
	f.discover = func(ctx context.Context, s media.Settings) ([]string, error) {
		// Open() permits only one connection: this read also proves that
		// the preview has released its SQLite transaction before SMB I/O.
		var n int
		if err := a.db.QueryRowContext(ctx, "SELECT count(*) FROM nas").Scan(&n); err != nil {
			t.Fatal(err)
		}
		seen = append(seen, s)
		return []string{"video", "IPC$", "电影"}, nil
	}
	preview := func(body string, expected media.Settings) {
		t.Helper()
		n := len(seen)
		w := req(a, admin, "POST", path, body)
		want(t, w, 200)
		if strings.TrimSpace(w.Body.String()) != `{"shares":["video","电影"]}` || len(seen) != n+1 || seen[n] != expected {
			t.Fatal("unexpected discovery response, call count, or credentials")
		}
	}
	saved := media.Settings{Host: "nas.local", Username: "player", Port: 445, Password: nasPW}
	preview(withPassword, saved)
	w = req(a, admin, "GET", nas, "")
	want(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != `{"nas":null}` {
		t.Fatal("preview persisted settings")
	}
	want(t, req(a, admin, "PUT", nas, `{`+identity+`,"share":"films","password":"`+nasPW+`"}`), 200)
	before := len(seen)
	for _, identity := range []string{`"host":"NAS.local","username":"player"`, `"host":"nas.local","username":"Player"`, identity + `,"domain":"work"`, identity + `,"port":1445`} {
		want(t, req(a, admin, "POST", path, `{`+identity+`}`), 400)
	}
	if len(seen) != before {
		t.Fatal("sent credentials to a different identity")
	}
	preview(`{"host":"other.local","username":"tester","password":"temporary"}`, media.Settings{Host: "other.local", Port: 445, Username: "tester", Password: "temporary"})
	preview(`{`+identity+`,"port":445,"domain":""}`, saved)
	want(t, req(a, admin, "PUT", nas, `{`+identity+`,"share":"other"}`), 400)
	f.discover = func(context.Context, media.Settings) ([]string, error) { return nil, nil }
	w = req(a, admin, "POST", path, withPassword)
	want(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != `{"shares":[]}` {
		t.Fatal("missing empty JSON array")
	}
	f.discover = func(context.Context, media.Settings) ([]string, error) {
		return nil, &media.Error{Status: 502, Code: "nas_unreachable", Message: "NAS 不可用"}
	}
	w = req(a, admin, "POST", path, withPassword)
	want(t, w, 502)
	if !strings.Contains(w.Body.String(), `"code":"nas_unreachable"`) {
		t.Fatal("lost typed media error")
	}
	a.opts.Media = &fakeMedia{}
	want(t, req(a, admin, "POST", path, withPassword), 503)
}
