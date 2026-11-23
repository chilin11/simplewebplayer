package player

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"path"
	"strings"
	"time"

	"simplewebplayer/internal/casting"
	"simplewebplayer/internal/media"
)

type publicDevice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Model    string `json:"model"`
	Protocol string `json:"protocol"`
}

// Resolve the original session and its current permission in one DB snapshot.
// Roots have immutable paths and autoincrement IDs; NAS identity cannot be
// changed while any roots exist (saveNAS). A ticket never stores NAS credentials.
func castRoot(tx *sql.Tx, owner castOwner, root int64) (base string, expires int64, err error) {
	err = tx.QueryRow(`SELECT r.path,s.expires FROM sessions s JOIN users u ON u.id=s.user_id JOIN roots r ON r.id=?
		WHERE s.hash=? AND s.expires>? AND u.enabled=1 AND (u.role='admin' OR EXISTS(SELECT 1 FROM grants g WHERE g.user_id=u.id AND g.root_id=r.id))`, root, []byte(owner.session), time.Now().Unix()).Scan(&base, &expires)
	return
}

func (c *call) castControl(tx *sql.Tx) error {
	a := c.app
	if a.opts.Cast == nil {
		return &apiError{503, "cast_unavailable", "投屏服务不可用"}
	}
	lan, ok := a.castLAN(c.r)
	if !ok {
		return &apiError{403, "cast_lan_only", "请通过同一局域网直接连接后再投屏"}
	}
	o := castOwner{account: c.user.ID, session: string(tokenHash(c.r)), network: networkOf(lan)}
	if c.r.URL.RawQuery != "" {
		return invalid("投屏控制不接受查询参数")
	}
	switch c.route.key {
	case "cast/discover":
		if _, err := fields(c.raw, ""); err != nil {
			return err
		}
		c.after = func() error {
			devices, err := a.opts.Cast.Discover(c.r.Context(), lan)
			if err != nil {
				return &apiError{502, "cast_discovery_failed", "无法搜索电视，请检查局域网连接"}
			}
			choices := make([]casting.Device, 0, castVisibleLimit)
			seen := make(map[castEndpoint]bool)
			for _, d := range devices {
				d.Addr = d.Addr.Unmap()
				if !d.AllowedOn(lan) {
					continue
				}
				key := endpoint(d)
				if seen[key] {
					continue
				}
				seen[key] = true
				if name, err := nameText(d.Name, 128); err == nil {
					d.Name = name
				} else {
					d.Name = "电视"
				}
				d.Model, _ = nameText(d.Model, 128)
				choices = append(choices, d)
				if len(choices) == castVisibleLimit {
					break
				}
			}
			// Logout may have committed during discovery. Do not repopulate a dead session.
			return a.transaction(c.r.Context(), func(tx *sql.Tx) error {
				u, err := sessionUser(tx, c.r)
				if err != nil {
					return err
				}
				if u == nil {
					return &apiError{401, "unauthorized", "请先登录"}
				}
				out, err := a.casts.remember(o, choices)
				if err != nil {
					return err
				}
				c.out = map[string]any{"devices": out}
				return nil
			})
		}
	case "cast/stop":
		obj, err := fields(c.raw, "castId")
		if err != nil {
			return err
		}
		id, err := get[string](obj, "castId")
		if err != nil {
			return err
		}
		c.after = func() error {
			t, err := a.casts.beginStop(o, id)
			if err != nil {
				return err
			}
			err = a.opts.Cast.Stop(c.r.Context(), lan, t.device, t.mediaURL(lan))
			a.casts.finishStop(t, err == nil)
			if err != nil {
				return &apiError{502, "cast_stop_failed", "已撤销取流授权，但未能确认" + receiverLabel(t.device) + "已停止"}
			}
			c.status = http.StatusNoContent
			return nil
		}
	case "cast/start":
		_, expires, err := castRoot(tx, o, c.id)
		if err != nil {
			return err
		}
		obj, err := fields(c.raw, "deviceId path startSeconds")
		if err != nil {
			return err
		}
		id, err := get[string](obj, "deviceId")
		if err != nil {
			return err
		}
		file, err := get[string](obj, "path")
		if err != nil {
			return err
		}
		if file, err = media.NormalizePath(file); err != nil {
			return err
		}
		ct := casting.ContentType(file)
		if ct == "" {
			return &apiError{415, "cast_unsupported_format", "投屏仅支持 MP4、M4V 或 WebM 文件"}
		}
		seconds, err := optional(obj, "startSeconds", 0.0)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > 7*24*60*60 {
			return invalid("起播时间无效")
		}
		if _, err := a.credentials(tx); err != nil {
			return err
		}
		if a.opts.Media == nil {
			return &apiError{503, "media_unavailable", "媒体服务不可用"}
		}
		c.after = func() error {
			t, err := a.casts.issue(o, id, c.id, file, expires)
			if err != nil {
				return err
			}
			m := casting.Media{URL: t.mediaURL(lan), Title: path.Base(file), ContentType: ct, StartSeconds: seconds}
			err = a.opts.Cast.Load(c.r.Context(), lan, t.device, m)
			a.casts.finishLoad(t, err == nil)
			if err != nil {
				return &apiError{502, "cast_load_failed", receiverLabel(t.device) + "未能开始播放，请检查连接和视频格式"}
			}
			c.out = map[string]any{"cast": map[string]string{"id": t.controlID, "deviceId": id, "deviceName": t.device.Name}}
			return nil
		}
	}
	return nil
}

func receiverLabel(d casting.Device) string {
	if d.Kind() == casting.ProtocolDLNA {
		return "DLNA 电视"
	}
	return "Google Cast 电视"
}

func castCORS(h http.Header) {
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("Access-Control-Allow-Origin", "*") // No cookies or credentialed CORS.
	h.Set("Access-Control-Expose-Headers", "Accept-Ranges, Content-Length, Content-Range, Content-Type")
}

func (c *call) castMedia() error {
	a, r, w := c.app, c.r, c.w
	token := c.route.id
	if len(token) != 43 || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.EscapedPath() != castMediaPath+token {
		return missing()
	}
	t, ok := a.casts.lookup(token)
	if !ok || casting.Peer(r) != t.device.Addr {
		return missing()
	}
	lan, ok := a.castLAN(r)
	if !ok || networkOf(lan) != t.owner.network {
		return missing()
	}
	var base string
	var settings media.Settings
	err := a.transaction(r.Context(), func(tx *sql.Tx) error {
		var err error
		base, _, err = castRoot(tx, t.owner, t.root)
		if errors.Is(err, sql.ErrNoRows) {
			h := []byte(t.owner.session)
			if u, e := sessionUserByHash(tx, h); e == nil {
				if u == nil {
					a.casts.forgetSession(h)
				} else {
					a.casts.loseGrant(t.token)
				}
			}
		}
		if err != nil {
			return err
		}
		settings, err = a.credentials(tx)
		return err
	})
	if err != nil {
		return missing()
	}
	// Stop or expiry may have won while the transaction was acquiring its lock.
	if _, ok := a.casts.lookup(token); !ok {
		return missing()
	}
	switch r.Method {
	case http.MethodOptions:
		method := r.Header.Get("Access-Control-Request-Method")
		if method != "" && method != "GET" && method != "HEAD" {
			return invalid("预检方法不支持")
		}
		for _, line := range r.Header.Values("Access-Control-Request-Headers") {
			for _, name := range strings.Split(line, ",") {
				if !strings.EqualFold(strings.TrimSpace(name), "Range") {
					return invalid("预检标头不支持")
				}
			}
		}
		castCORS(w.Header())
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "Range")
		if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	case http.MethodGet, http.MethodHead:
		sw := &streamWriter{ResponseWriter: w, castType: casting.ContentType(t.path)}
		if t.device.Kind() == casting.ProtocolDLNA {
			sw.dlna = true
			sw.dlnaFeatures = r.Header.Get("GetContentFeatures.dlna.org") == "1"
		}
		err := a.opts.Media.Stream(sw, r, settings, base, t.path)
		if sw.started {
			return nil
		}
		var me *media.Error
		if errors.As(err, &me) && me.Status >= 500 {
			return &apiError{503, "media_unavailable", "媒体服务暂时不可用"}
		}
		return missing()
	default:
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		return &apiError{405, "method_not_allowed", "请求方法不支持"}
	}
}
