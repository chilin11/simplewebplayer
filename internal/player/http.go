package player

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"modernc.org/sqlite"
	"simplewebplayer/internal/media"
)

type apiError struct {
	status    int
	code, msg string
}

func (e *apiError) Error() string { return e.msg }
func invalid(msg string) error    { return &apiError{400, "invalid_request", msg} }
func missing() error              { return &apiError{404, "not_found", "资源不存在"} }
func conflict(msg string) error   { return &apiError{409, "conflict", msg} }
func writeError(w http.ResponseWriter, err error) {
	e := &apiError{500, "internal_error", "服务暂时不可用"}
	var pe *apiError
	var me *media.Error
	switch {
	case errors.As(err, &pe):
		e = pe
	case errors.As(err, &me):
		e = &apiError{me.Status, me.Code, me.Message}
	case errors.Is(err, sql.ErrNoRows):
		e = missing().(*apiError)
	}
	writeJSON(w, e.status, map[string]any{"error": map[string]string{"code": e.code, "message": e.msg}})
}
func writeJSON(w http.ResponseWriter, status int, data any) {
	if status == 204 {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func unique(err error, msg string) error {
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code() == 2067 {
		return conflict(msg)
	}
	return err
}

type object map[string]json.RawMessage

// Exact keys and types; reject null, duplicates, a second value and invalid UTF-8.
func fields(raw []byte, allowed string) (object, error) {
	bad := invalid("JSON 字段或格式无效")
	if !utf8.Valid(raw) {
		return nil, bad
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return nil, bad
	}
	out := object{}
	allow := " " + allowed + " "
	for d.More() {
		k, e := d.Token()
		key, ok := k.(string)
		if e != nil || !ok || key == "" || strings.ContainsRune(key, ' ') || !strings.Contains(allow, " "+key+" ") {
			return nil, bad
		}
		if _, dup := out[key]; dup {
			return nil, bad
		}
		var val json.RawMessage
		if e = d.Decode(&val); e != nil || bytes.Equal(val, []byte("null")) {
			return nil, bad
		}
		out[key] = val
	}
	if _, e = d.Token(); e != nil {
		return nil, bad
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, bad
	}
	return out, nil
}
func get[T any](obj object, key string) (T, error) {
	var v T
	raw, ok := obj[key]
	if !ok || json.Unmarshal(raw, &v) != nil {
		return v, invalid("字段缺失或类型无效：" + key)
	}
	return v, nil
}
func optional[T any](obj object, key string, def T) (T, error) {
	if _, ok := obj[key]; !ok {
		return def, nil
	}
	return get[T](obj, key)
}
func nameText(s string, n int) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > n || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", invalid("名称无效")
	}
	return s, nil
}

type route struct {
	key, methods, id string
	admin, private   bool
}

func (rt route) isMediaStream() bool { return rt.key == "stream" || rt.key == "download" }

func match(path string) route {
	p := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(p) < 2 || p[0] != "api" {
		return route{}
	}
	var rt route
	switch p[1] {
	case "session", "login", "logout":
		if len(p) == 2 {
			rt.key = p[1]
			rt.methods = "POST"
			if rt.key == "session" {
				rt.methods = "GET"
			}
		}
	case "library":
		rt.private = true
		if len(p) == 2 {
			rt.key = "library"
			rt.methods = "GET"
		} else if len(p) == 4 {
			switch p[3] {
			case "browse":
				rt.key, rt.id, rt.methods = p[3], p[2], "GET"
			case "stream", "download":
				rt.key, rt.id, rt.methods = p[3], p[2], "GET HEAD"
			}
		}
	case "admin":
		rt.admin = true
		rt.private = true
		if len(p) < 3 {
			return rt
		}
		switch p[2] {
		case "users", "roots":
			if len(p) == 3 {
				rt.key = p[2]
				rt.methods = "GET POST"
			} else if len(p) == 4 {
				rt.key = p[2] + "/id"
				rt.methods = "PATCH DELETE"
				rt.id = p[3]
			} else if len(p) == 5 && p[2] == "users" && p[4] == "grants" {
				rt.key = "grants"
				rt.methods = "GET PUT"
				rt.id = p[3]
			}
		case "nas":
			if len(p) == 3 {
				rt.key = "nas"
				rt.methods = "GET PUT"
			} else if len(p) == 4 {
				switch p[3] {
				case "browse":
					rt.key = "nas/browse"
					rt.methods = "GET"
				case "test":
					rt.key = "nas/test"
					rt.methods = "POST"
				}
			}
		}
	}
	return rt
}

type call struct {
	app    *App
	w      http.ResponseWriter
	r      *http.Request
	route  route
	id     int64
	raw    []byte
	user   *User
	out    any
	status int
	after  func() error
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	c := &call{app: a, w: w, r: r, route: match(r.URL.Path), status: 200}
	if err := c.run(); err != nil {
		writeError(w, err)
		return
	}
	if c.after != nil {
		if err := c.after(); err != nil {
			writeError(w, err)
			return
		}
		if c.route.isMediaStream() {
			return
		}
	}
	writeJSON(w, c.status, c.out)
}
func (c *call) run() error {
	r := c.r
	rt := c.route
	if rt.key == "" {
		return missing()
	}
	if !strings.Contains(" "+rt.methods+" ", " "+r.Method+" ") {
		c.w.Header().Set("Allow", strings.ReplaceAll(rt.methods, " ", ", "))
		return &apiError{405, "method_not_allowed", "请求方法不支持"}
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		if r.Header.Get("X-Player-Request") != "1" {
			return &apiError{403, "request_header_required", "缺少请求标头"}
		}
		ct, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || ct != "application/json" {
			return &apiError{415, "json_required", "需要 application/json"}
		}
		const limit = 65536
		raw, e := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if e != nil {
			return invalid("无法读取请求")
		}
		if len(raw) > limit {
			return &apiError{413, "request_too_large", "请求过大"}
		}
		if len(raw) == 0 && r.Method == "DELETE" {
			raw = []byte("{}")
		}
		c.raw = raw
	}
	if rt.key == "login" {
		return c.login()
	}
	return c.app.transaction(r.Context(), func(tx *sql.Tx) error {
		u, e := sessionUser(tx, r)
		if e != nil {
			return e
		}
		c.user = u
		if rt.private && u == nil {
			return &apiError{401, "unauthorized", "请先登录"}
		}
		if rt.admin && u.Role != "admin" {
			return &apiError{403, "forbidden", "需要管理员权限"}
		}
		if rt.id != "" {
			id, e := strconv.ParseInt(rt.id, 10, 64)
			if e != nil || id < 1 {
				return missing()
			}
			c.id = id
		}
		switch rt.key {
		case "session":
			c.out = map[string]any{"user": u}
		case "logout":
			return c.logout(tx)
		case "users", "users/id", "grants":
			return c.accounts(tx)
		case "roots", "roots/id", "nas":
			return c.settings(tx)
		default:
			return c.library(tx)
		}
		return nil
	})
}
