package player

import (
	"database/sql"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"simplewebplayer/internal/media"
)

type libraryRoot struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

func queryPath(raw string) (string, error) {
	q, e := url.ParseQuery(raw)
	if e != nil {
		return "", invalid("无效的查询参数")
	}
	if len(q) == 0 {
		return "", nil
	}
	vs, ok := q["path"]
	if !ok || len(q) != 1 || len(vs) != 1 {
		return "", invalid("仅支持单个 path 参数")
	}
	return media.NormalizePath(vs[0])
}

// Even if an adapter fails after sending media, never append JSON to the byte stream.
type streamWriter struct {
	http.ResponseWriter
	started    bool
	attachment string
}

func (w *streamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *streamWriter) start(status int) {
	// Wait until the first media response is committed; errors and 416 are not downloads.
	if !w.started && w.attachment != "" && (status == http.StatusOK || status == http.StatusPartialContent) {
		w.Header().Set("Content-Disposition", w.attachment)
	}
	w.started = true
}

func (w *streamWriter) WriteHeader(status int) {
	w.start(status)
	w.ResponseWriter.WriteHeader(status)
}
func (w *streamWriter) Write(b []byte) (int, error) {
	w.start(http.StatusOK)
	return w.ResponseWriter.Write(b)
}
func (w *streamWriter) Flush() {
	w.start(http.StatusOK)
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (c *call) library(tx *sql.Tx) error {
	key := c.route.key
	if key == "library" {
		rows, e := tx.Query(`SELECT r.id,r.label FROM roots r WHERE ?='admin' OR EXISTS(SELECT 1 FROM grants g WHERE g.root_id=r.id AND g.user_id=?) ORDER BY r.id`, c.user.Role, c.user.ID)
		if e != nil {
			return e
		}
		defer rows.Close()
		roots := []libraryRoot{}
		for rows.Next() {
			var x libraryRoot
			if e := rows.Scan(&x.ID, &x.Label); e != nil {
				return e
			}
			roots = append(roots, x)
		}
		c.out = map[string]any{"roots": roots}
		return rows.Err()
	}
	var base, path string
	var e error
	if key == "browse" || c.route.isMediaStream() {
		e = tx.QueryRow(`SELECT r.path FROM roots r WHERE r.id=? AND (?='admin' OR EXISTS(SELECT 1 FROM grants g WHERE g.root_id=r.id AND g.user_id=?))`, c.id, c.user.Role, c.user.ID).Scan(&base)
		if e != nil {
			return e
		}
	}
	if key == "nas/test" {
		if _, e = fields(c.raw, ""); e != nil {
			return e
		}
	} else {
		if path, e = queryPath(c.r.URL.RawQuery); e != nil {
			return e
		}
	}
	settings, e := c.app.credentials(tx)
	if e != nil {
		return e
	}
	backend := c.app.opts.Media
	if backend == nil {
		return &apiError{503, "media_unavailable", "媒体服务不可用"}
	}
	c.after = func() error {
		if c.route.isMediaStream() {
			w := &streamWriter{ResponseWriter: c.w}
			if key == "download" {
				// queryPath already decoded and validated the slash-delimited path.
				if name := path[strings.LastIndexByte(path, '/')+1:]; name != "" {
					w.attachment = mime.FormatMediaType("attachment", map[string]string{"filename": name})
				}
			}
			e := backend.Stream(w, c.r, settings, base, path)
			if w.started {
				return nil
			}
			return e
		}
		if key == "nas/test" {
			if err := backend.Test(c.r.Context(), settings); err != nil {
				return err
			}
			c.out = map[string]bool{"ok": true}
			return nil
		}
		entries, e := backend.List(c.r.Context(), settings, base, path)
		if e != nil {
			return e
		}
		visible := make([]media.Entry, 0, len(entries))
		for _, v := range entries {
			if v.Kind == "directory" || v.Kind == "video" {
				visible = append(visible, v)
			}
		}
		c.out = map[string]any{"path": path, "entries": visible}
		return nil
	}
	return nil
}
