package player

import "database/sql"

func validRole(r string) error {
	if r != "viewer" && r != "admin" {
		return invalid("角色必须是 viewer 或 admin")
	}
	return nil
}
func (c *call) accounts(tx *sql.Tx) error {
	method := c.r.Method
	if c.route.key == "users" {
		if method == "GET" {
			rows, e := tx.Query("SELECT id,username,role,enabled FROM users ORDER BY id")
			if e != nil {
				return e
			}
			defer rows.Close()
			users := []*User{}
			for rows.Next() {
				u, e := readUser(rows)
				if e != nil {
					return e
				}
				users = append(users, u)
			}
			c.out = map[string]any{"users": users}
			return rows.Err()
		}
		obj, e := fields(c.raw, "username password role")
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
		role, e := get[string](obj, "role")
		if e != nil {
			return e
		}
		if e = validRole(role); e != nil {
			return e
		}
		pw, e := get[string](obj, "password")
		if e != nil {
			return e
		}
		hash, e := passwordHash(pw)
		if e != nil {
			return e
		}
		res, e := tx.Exec("INSERT INTO users(username,hash,role) VALUES(?,?,?)", name, hash, role)
		if e != nil {
			return unique(e, "用户名已存在")
		}
		id, e := res.LastInsertId()
		if e != nil {
			return e
		}
		c.out = map[string]any{"user": User{id, name, role, true}}
		c.status = 201
		return nil
	}
	old, e := userByID(tx, c.id)
	if e != nil {
		return e
	}
	if c.route.key == "grants" {
		return c.grants(tx)
	}
	del := method == "DELETE"
	keys := "password enabled role"
	if del {
		keys = ""
	}
	obj, e := fields(c.raw, keys)
	if e != nil {
		return e
	}
	if !del && len(obj) == 0 {
		return invalid("未提供更改字段")
	}
	enabled, e := optional(obj, "enabled", old.Enabled)
	if e != nil {
		return e
	}
	role, e := optional(obj, "role", old.Role)
	if e != nil {
		return e
	}
	if e = validRole(role); e != nil {
		return e
	}
	if old.Enabled && old.Role == "admin" && (del || !enabled || role != "admin") {
		var other bool
		e = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM users WHERE enabled=1 AND role='admin' AND id<>?)", c.id).Scan(&other)
		if e != nil {
			return e
		}
		if !other {
			return conflict("至少需要保留一位启用的管理员")
		}
	}
	if del {
		_, e = tx.Exec("DELETE FROM users WHERE id=?", c.id)
		c.after = c.app.casts.onAccountReset(c.id)
		c.status = 204
		return e
	}
	_, changedPassword := obj["password"]
	if changedPassword {
		pw, e := get[string](obj, "password")
		if e != nil {
			return e
		}
		hash, e := passwordHash(pw)
		if e != nil {
			return e
		}
		if _, e = tx.Exec("UPDATE users SET hash=? WHERE id=?", hash, c.id); e != nil {
			return e
		}
	}
	if _, e = tx.Exec("UPDATE users SET role=?,enabled=? WHERE id=?", role, enabled, c.id); e != nil {
		return e
	}
	if changedPassword || old.Role != role || old.Enabled != enabled {
		if _, e = tx.Exec("DELETE FROM sessions WHERE user_id=?", c.id); e != nil {
			return e
		}
		c.after = c.app.casts.onAccountReset(c.id)
	}
	old.Enabled = enabled
	old.Role = role
	c.out = map[string]any{"user": old}
	return nil
}
func (c *call) grants(tx *sql.Tx) error {
	if c.r.Method == "PUT" {
		obj, e := fields(c.raw, "rootIds")
		if e != nil {
			return e
		}
		ids, e := get[[]int64](obj, "rootIds")
		if e != nil {
			return e
		}
		if _, e = tx.Exec("DELETE FROM grants WHERE user_id=?", c.id); e != nil {
			return e
		}
		seen := map[int64]bool{}
		for _, id := range ids {
			if id < 1 {
				return invalid("目录 ID 无效")
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			res, e := tx.Exec("INSERT INTO grants(user_id,root_id) SELECT ?,id FROM roots WHERE id=?", c.id, id)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil {
				return e
			}
			if n != 1 {
				return invalid("目录 ID 不存在")
			}
		}
	}
	rows, e := tx.Query("SELECT root_id FROM grants WHERE user_id=? ORDER BY root_id", c.id)
	if e != nil {
		return e
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if e := rows.Scan(&id); e != nil {
			return e
		}
		ids = append(ids, id)
	}
	c.out = map[string]any{"rootIds": ids}
	return rows.Err()
}
