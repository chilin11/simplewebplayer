package player

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPersistentBrowserSession(t *testing.T) {
	dir := t.TempDir()
	a, original := setup(t, dir, nil)
	checkCookie := func(w *httptest.ResponseRecorder, value string) {
		t.Helper()
		cookies := w.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatal("missing session cookie")
		}
		c := cookies[0]
		if c.Value != value || c.MaxAge != sessionCookieAge || c.Expires.Before(time.Now().Add(364*24*time.Hour)) || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
			t.Fatal("session cookie did not retain its value, lifetime, or protections")
		}
	}
	checkExpiry := func(app *App, cookie *http.Cookie, expected int64) {
		t.Helper()
		var expiry int64
		if err := app.db.QueryRow("SELECT expires FROM sessions WHERE hash=?", digest(cookie.Value)).Scan(&expiry); err != nil || expiry != expected {
			t.Fatalf("session expiry=%d, want %d, error=%v", expiry, expected, err)
		}
	}
	if original.MaxAge != sessionCookieAge {
		t.Fatal("new login is not persistent")
	}
	checkExpiry(a, original, persistentSessionExpiry)

	// Model an active session created by a previous release with a fixed expiry.
	if _, err := a.db.Exec("UPDATE sessions SET expires=? WHERE hash=?", time.Now().Add(time.Hour).Unix(), digest(original.Value)); err != nil {
		t.Fatal(err)
	}
	renewed := req(a, original, "GET", "/api/session", "")
	want(t, renewed, 200)
	checkCookie(renewed, original.Value)
	checkExpiry(a, original, persistentSessionExpiry)

	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := Open(dir, Options{SecureCookies: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	checkCookie(req(a, original, "GET", "/api/session", ""), original.Value)
	want(t, req(a, original, "GET", "/api/admin/users", ""), 200)

	stale := signIn(t, a, "first")
	staleTime := time.Now().Add(-time.Minute).Unix()
	if _, err := a.db.Exec("UPDATE sessions SET expires=? WHERE hash=?", staleTime, digest(stale.Value)); err != nil {
		t.Fatal(err)
	}
	checkAnonymous := func(cookie *http.Cookie) {
		t.Helper()
		w := req(a, cookie, "GET", "/api/session", "")
		want(t, w, 200)
		if !bytes.Contains(w.Body.Bytes(), []byte(`"user":null`)) || len(w.Result().Cookies()) != 0 {
			t.Fatal("invalid session was accepted or renewed")
		}
	}
	checkAnonymous(stale)
	checkExpiry(a, stale, staleTime)

	logout := req(a, original, "POST", "/api/logout", "{}")
	want(t, logout, 204)
	if cookies := logout.Result().Cookies(); len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatal("logout did not clear the device cookie")
	}
	checkAnonymous(original)

	// A password change revokes all of this account's device sessions.
	browserOne := signIn(t, a, "first")
	browserTwo := signIn(t, a, "first")
	want(t, req(a, browserOne, "PATCH", "/api/admin/users/1", `{"password":"replacement-fixture"}`), 200)
	checkAnonymous(browserOne)
	checkAnonymous(browserTwo)
}
