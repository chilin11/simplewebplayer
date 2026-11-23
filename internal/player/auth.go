package player

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/scrypt"
)

const cookieName = "player_session"

// Keep the existing integer expiry column and its comparisons compatible with
// previous releases. This distant sentinel denotes a session without a fixed
// server expiration; account and logout revocations still delete the row.
const persistentSessionExpiry int64 = 253402300799 // 9999-12-31 UTC

// Browsers can cap cookie retention even when the server session has no expiry.
// Refresh the cookie when an authenticated browser opens the app.
const sessionCookieAge = 365 * 24 * 60 * 60

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Enabled  bool   `json:"enabled"`
}
type scanner interface{ Scan(...any) error }

func readUser(s scanner) (*User, error) {
	var u User
	e := s.Scan(&u.ID, &u.Username, &u.Role, &u.Enabled)
	return &u, e
}
func userByID(tx *sql.Tx, id int64) (*User, error) {
	return readUser(tx.QueryRow("SELECT id,username,role,enabled FROM users WHERE id=?", id))
}
func passwordHash(password string) ([]byte, error) {
	if strings.TrimSpace(password) == "" {
		return nil, invalid("密码不能为空或全是空白字符")
	}
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) > 256 {
		return nil, invalid("请使用不超过 256 个字符的有效密码")
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	key, e := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if e != nil {
		return nil, e
	}
	return append(append([]byte{1}, salt...), key...), nil
}
func passwordMatches(hash []byte, password string) bool {
	if len(hash) != 49 || hash[0] != 1 {
		return false
	}
	key, e := scrypt.Key([]byte(password), hash[1:17], 32768, 8, 1, 32)
	return e == nil && subtle.ConstantTimeCompare(hash[17:], key) == 1
}

// HasAdmin reports whether initial administrator setup has already completed.
func (a *App) HasAdmin() (bool, error) {
	var found bool
	err := a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM users WHERE role='admin')").Scan(&found)
	return found, err
}

func (a *App) InitAdmin(username, password string) error {
	name, e := nameText(username, 64)
	if e != nil {
		return e
	}
	hash, e := passwordHash(password)
	if e != nil {
		return e
	}
	return a.transaction(context.Background(), func(tx *sql.Tx) error {
		var n int
		if e := tx.QueryRow("SELECT COUNT(*) FROM users WHERE role='admin'").Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			return conflict("管理员已经存在")
		}
		_, e := tx.Exec("INSERT INTO users(username,hash,role) VALUES(?,?,'admin')", name, hash)
		return unique(e, "用户名已存在")
	})
}
func digest(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
func tokenHash(r *http.Request) []byte {
	c, e := r.Cookie(cookieName)
	if e != nil || len(c.Value) != 43 {
		return nil
	}
	return digest(c.Value)
}
func sessionUser(tx *sql.Tx, r *http.Request) (*User, error) {
	return sessionUserByHash(tx, tokenHash(r))
}
func sessionUserByHash(tx *sql.Tx, h []byte) (*User, error) {
	if h == nil {
		return nil, nil
	}
	u, e := readUser(tx.QueryRow(`SELECT u.id,u.username,u.role,u.enabled FROM users u JOIN sessions s ON u.id=s.user_id WHERE s.hash=? AND s.expires>? AND u.enabled=1`, h, time.Now().Unix()))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return u, e
}
func (c *call) setCookie(value string, age int) {
	cookie := &http.Cookie{Name: cookieName, Value: value, MaxAge: age, Path: "/", HttpOnly: true, Secure: c.app.opts.SecureCookies, SameSite: http.SameSiteLaxMode}
	if age < 0 {
		cookie.Expires = time.Unix(1, 0).UTC()
	} else if age > 0 {
		cookie.Expires = time.Now().UTC().Add(time.Duration(age) * time.Second)
	}
	http.SetCookie(c.w, cookie)
}

// Called only after sessionUser validated this cookie in the same transaction.
// Already expired and revoked sessions never reach this method.
func (c *call) renewSession(tx *sql.Tx) error {
	cookie, err := c.r.Cookie(cookieName)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE sessions SET expires=? WHERE hash=? AND expires<>?", persistentSessionExpiry, digest(cookie.Value), persistentSessionExpiry); err != nil {
		return err
	}
	c.after = func() error {
		c.setCookie(cookie.Value, sessionCookieAge)
		return nil
	}
	return nil
}

func (c *call) logout(tx *sql.Tx) error {
	if _, e := fields(c.raw, ""); e != nil {
		return e
	}
	if _, e := tx.Exec("DELETE FROM sessions WHERE hash=?", tokenHash(c.r)); e != nil {
		return e
	}
	c.status = 204
	c.after = func() error { c.app.casts.forgetSession(tokenHash(c.r)); c.setCookie("", -1); return nil }
	return nil
}
func limited() error { return &apiError{429, "rate_limited", "登录尝试过多，请稍后再试"} }
func consume(tx *sql.Tx, key []byte, max int, now int64) error {
	res, e := tx.Exec(`INSERT INTO attempts(key,deadline,n) VALUES(?,?,1) ON CONFLICT(key) DO UPDATE SET n=n+1 WHERE n<?`, key, now+900, max)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e == nil && n == 0 {
		return limited()
	}
	return e
}
func (c *call) login() error {
	obj, e := fields(c.raw, "username password")
	if e != nil {
		return e
	}
	name, e := get[string](obj, "username")
	if e != nil {
		return e
	}
	name, e = nameText(name, 64)
	if e != nil {
		return e
	}
	pw, e := get[string](obj, "password")
	if e != nil {
		return e
	}
	failed := &apiError{401, "invalid_credentials", "用户名或密码错误"}
	if pw == "" || utf8.RuneCountInString(pw) > 256 {
		return failed
	}
	a := c.app
	select {
	case a.hashSlots <- struct{}{}:
		defer func() { <-a.hashSlots }()
	default:
		return limited()
	}
	now := time.Now().Unix()
	var id int64
	var hash []byte
	var account []byte
	e = a.transaction(c.r.Context(), func(tx *sql.Tx) error {
		if _, e := tx.Exec("DELETE FROM attempts WHERE deadline<=?", now); e != nil {
			return e
		}
		e := tx.QueryRow("SELECT id,hash FROM users WHERE username=? AND enabled=1", name).Scan(&id, &hash)
		if errors.Is(e, sql.ErrNoRows) {
			hash = a.dummy
		} else if e != nil {
			return e
		}
		// Only the socket peer is trusted. A local tunnel collapses clients to
		// loopback; retain the global hash budget, and enforce per-client rate
		// limits at the tunnel edge rather than let one visitor lock any account.
		host, _, _ := net.SplitHostPort(c.r.RemoteAddr)
		if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
			peer := ip.String()
			if e := consume(tx, digest("peer:"+peer), 240, now); e != nil {
				return e
			}
			bucket := digest("account:" + strings.ToLower(name))[0]
			key := "unknown:" + string([]byte{bucket})
			if id != 0 {
				key = "account:" + strconv.FormatInt(id, 10)
			}
			account = digest(peer + "\x00" + key)
			return consume(tx, account, 15, now)
		}
		return nil
	})
	if e != nil {
		return e
	}
	if !passwordMatches(hash, pw) || id == 0 {
		return failed
	}
	raw := make([]byte, 32)
	rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	e = a.transaction(c.r.Context(), func(tx *sql.Tx) error {
		u, e := readUser(tx.QueryRow("SELECT id,username,role,enabled FROM users WHERE id=? AND hash=? AND enabled=1", id, hash))
		if errors.Is(e, sql.ErrNoRows) {
			return failed
		}
		if e != nil {
			return e
		}
		if _, e = tx.Exec("DELETE FROM sessions WHERE expires<=? OR hash=?", now, tokenHash(c.r)); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO sessions(hash,user_id,expires) VALUES(?,?,?)", digest(token), id, persistentSessionExpiry); e != nil {
			return e
		}
		if account != nil {
			if _, e = tx.Exec("DELETE FROM attempts WHERE key=?", account); e != nil {
				return e
			}
		}
		c.out = map[string]any{"user": u}
		return nil
	})
	if e != nil {
		return e
	}
	a.casts.forgetSession(tokenHash(c.r)) // successful login rotated the previous session
	c.setCookie(token, sessionCookieAge)
	return nil
}
