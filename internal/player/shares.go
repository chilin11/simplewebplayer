package player

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"simplewebplayer/internal/media"
)

func (c *call) previewShares(tx *sql.Tx) error {
	obj, err := fields(c.raw, "host port username domain password")
	if err != nil {
		return err
	}
	s, err := nasServerFields(obj)
	if err != nil {
		return err
	}
	if err = media.ValidateServer(s); err != nil {
		return invalid(err.Error())
	}
	browser, ok := c.app.opts.Media.(media.ShareBrowser)
	if !ok {
		return &apiError{503, "media_unavailable", "媒体服务不支持枚举共享"}
	}
	if _, supplied := obj["password"]; supplied {
		s.Password, err = nasPassword(obj)
	} else {
		// Match the exact endpoint and account before reading any secret.
		// Only preview ignores share: it enumerates the server, not a share.
		var secret []byte
		err = tx.QueryRow(`SELECT secret FROM nas WHERE id=1 AND host=? AND port=? AND username=? AND domain=?`, s.Host, s.Port, s.Username, s.Domain).Scan(&secret)
		if errors.Is(err, sql.ErrNoRows) {
			return invalid("请为此 NAS 地址和账号输入 SMB 密码")
		}
		if err == nil {
			s.Password, err = c.app.openNASPassword(secret)
		}
	}
	if err != nil {
		return err
	}
	c.after = func() error {
		select {
		case c.app.shareSlots <- struct{}{}:
			defer func() { <-c.app.shareSlots }()
		default:
			return &apiError{429, "nas_busy", "共享查询正在进行，请稍后重试"}
		}
		ctx, cancel := context.WithTimeout(c.r.Context(), 8*time.Second)
		defer cancel()
		names, err := browser.DiscoverShares(ctx, s)
		if err != nil {
			return err
		}
		shares := make([]string, 0, len(names))
		for _, name := range names {
			if media.IsShareName(name) {
				shares = append(shares, name)
			}
		}
		c.out = map[string][]string{"shares": shares}
		return nil
	}
	return nil
}
