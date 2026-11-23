package player

import (
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"simplewebplayer/internal/media"
)

type root struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
}
type nasRow struct {
	settings media.Settings
	secret   []byte
}

func readNAS(tx *sql.Tx) (*nasRow, error) {
	var n nasRow
	s := &n.settings
	e := tx.QueryRow("SELECT host,port,share,username,domain,secret FROM nas WHERE id=1").Scan(&s.Host, &s.Port, &s.Share, &s.Username, &s.Domain, &n.secret)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return &n, e
}
func (n *nasRow) public() any {
	if n == nil {
		return nil
	}
	return struct {
		media.Settings
		HasPassword bool `json:"hasPassword"`
	}{n.settings, len(n.secret) > 0}
}

var nasAAD = []byte("simplewebplayer/nas-password/v1")

func (a *App) credentials(tx *sql.Tx) (media.Settings, error) {
	n, e := readNAS(tx)
	if e != nil {
		return media.Settings{}, e
	}
	if n == nil {
		return media.Settings{}, &apiError{409, "nas_not_configured", "请先配置 NAS"}
	}
	pw, e := a.box.Open(nil, nil, n.secret, nasAAD)
	if e != nil {
		return media.Settings{}, errors.New("stored credential cannot be opened")
	}
	n.settings.Password = string(pw)
	return n.settings, nil
}
func (c *call) saveNAS(tx *sql.Tx, old *nasRow, obj object) error {
	var s media.Settings
	var e error
	if s.Host, e = get[string](obj, "host"); e != nil {
		return e
	}
	if s.Port, e = optional(obj, "port", 445); e != nil {
		return e
	}
	if s.Share, e = get[string](obj, "share"); e != nil {
		return e
	}
	if s.Username, e = get[string](obj, "username"); e != nil {
		return e
	}
	if s.Domain, e = optional(obj, "domain", ""); e != nil {
		return e
	}
	if e = media.ValidateSettings(s); e != nil {
		return invalid(e.Error())
	}
	if old != nil && old.settings != s {
		var exists bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM roots)").Scan(&exists); e != nil {
			return e
		}
		if exists {
			return conflict("请先删除已有目录，再修改 NAS 连接身份")
		}
	}
	var secret []byte
	if _, ok := obj["password"]; ok {
		pw, e := get[string](obj, "password")
		if e != nil {
			return e
		}
		if pw == "" || utf8.RuneCountInString(pw) > 1024 || strings.ContainsRune(pw, 0) {
			return invalid("NAS 密码无效")
		}
		secret = c.app.box.Seal(nil, nil, []byte(pw), nasAAD)
	} else if old != nil && old.settings == s {
		// Never authenticate to a changed endpoint or account with a secret
		// the administrator only intended to keep for the previous identity.
		secret = old.secret
	} else if old != nil {
		return invalid("更换 NAS 连接信息时，请重新输入 SMB 密码")
	} else {
		return invalid("首次配置须输入 NAS 密码")
	}
	_, e = tx.Exec(`INSERT OR REPLACE INTO nas(id,host,port,share,username,domain,secret) VALUES(1,?,?,?,?,?,?)`, s.Host, s.Port, s.Share, s.Username, s.Domain, secret)
	if e == nil {
		c.out = map[string]any{"nas": (&nasRow{s, secret}).public()}
	}
	return e
}
func (c *call) settings(tx *sql.Tx) error {
	if c.route.key == "nas" {
		old, e := readNAS(tx)
		if e != nil {
			return e
		}
		if c.r.Method == "GET" {
			c.out = map[string]any{"nas": old.public()}
			return nil
		}
		obj, e := fields(c.raw, "host port share username domain password")
		if e != nil {
			return e
		}
		return c.saveNAS(tx, old, obj)
	}
	if c.route.key == "roots" && c.r.Method == "GET" {
		rows, e := tx.Query("SELECT id,label,path FROM roots ORDER BY id")
		if e != nil {
			return e
		}
		defer rows.Close()
		roots := []root{}
		for rows.Next() {
			var x root
			if e := rows.Scan(&x.ID, &x.Label, &x.Path); e != nil {
				return e
			}
			roots = append(roots, x)
		}
		c.out = map[string]any{"roots": roots}
		return rows.Err()
	}
	var x root
	if c.route.key == "roots/id" {
		e := tx.QueryRow("SELECT id,label,path FROM roots WHERE id=?", c.id).Scan(&x.ID, &x.Label, &x.Path)
		if e != nil {
			return e
		}
		if c.r.Method == "DELETE" {
			if _, e = fields(c.raw, ""); e != nil {
				return e
			}
			_, e = tx.Exec("DELETE FROM roots WHERE id=?", c.id)
			c.status = 204
			return e
		}
	}
	keys := "label"
	create := c.route.key == "roots"
	if create {
		keys = "label path"
	}
	obj, e := fields(c.raw, keys)
	if e != nil {
		return e
	}
	if x.Label, e = get[string](obj, "label"); e != nil {
		return e
	}
	if x.Label, e = nameText(x.Label, 120); e != nil {
		return e
	}
	if create {
		var exists bool
		if e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM nas)").Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return conflict("请先配置 NAS")
		}
		if x.Path, e = get[string](obj, "path"); e != nil {
			return e
		}
		if x.Path, e = media.NormalizePath(x.Path); e != nil {
			return e
		}
		res, e := tx.Exec("INSERT INTO roots(label,path) VALUES(?,?)", x.Label, x.Path)
		if e != nil {
			return unique(e, "目录已经存在")
		}
		x.ID, e = res.LastInsertId()
		if e != nil {
			return e
		}
		c.status = 201
	} else {
		if _, e = tx.Exec("UPDATE roots SET label=? WHERE id=?", x.Label, x.ID); e != nil {
			return e
		}
	}
	c.out = map[string]any{"root": x}
	return nil
}
