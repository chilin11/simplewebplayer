package nas

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"simplewebplayer/internal/media"
)

const (
	streamChunk  = 64 * 1024
	writeTimeout = 15 * time.Second
)

// decimal parses nonnegative ASCII numerals and saturates at limit, including
// for values beyond int64. It still validates all digits after saturation.
func decimal(s string, limit int64) (int64, bool) {
	if s == "" {
		return 0, false
	}
	var n int64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		digit := int64(s[i] - '0')
		if n > limit/10 || (n == limit/10 && digit > limit%10) {
			n = limit
		} else {
			n = 10*n + digit
		}
	}
	return n, true
}

// byteRange returns one inclusive range, or false for a malformed or
// unsatisfiable header. Multipart ranges and duplicate Range fields are invalid.
func byteRange(fields []string, size int64) (start, end int64, ok bool) {
	if len(fields) != 1 || size <= 0 {
		return 0, 0, false
	}
	unit, spec, hasEqual := strings.Cut(strings.Trim(fields[0], " \t"), "=")
	first, last, hasDash := strings.Cut(spec, "-")
	if !hasEqual || !strings.EqualFold(unit, "bytes") || !hasDash {
		return 0, 0, false
	}
	if first == "" {
		n, valid := decimal(last, size)
		return size - n, size - 1, valid && n > 0
	}
	start, ok = decimal(first, size)
	if !ok || start >= size {
		return 0, 0, false
	}
	end = size - 1
	if last != "" {
		end, ok = decimal(last, size-1)
	}
	return start, end, ok && start <= end
}

func (b *Backend) Stream(w http.ResponseWriter, r *http.Request, cfg media.Settings, base, relative string) error {
	target, err := targetPath(base, relative)
	if err != nil {
		return err
	}
	mime := videoMIME[strings.ToLower(path.Ext(relative))]
	if mime == "" {
		return problem(http.StatusUnsupportedMediaType, "unsupported_media", "不支持此视频格式")
	}
	head := r.Method == http.MethodHead
	if !head && r.Method != http.MethodGet {
		return problem(http.StatusMethodNotAllowed, "method_not_allowed", "不支持此请求方法")
	}
	if !head {
		select {
		case b.streams <- struct{}{}:
			defer func() { <-b.streams }()
		default:
			return problem(http.StatusServiceUnavailable, "too_many_streams", "同时播放的视频过多")
		}
	}
	v, err := b.connect(r.Context(), cfg)
	if err != nil {
		return publicError(err)
	}
	defer v.Close()
	f, info, err := openChecked(v, target, false)
	if err != nil {
		return err
	}
	defer f.Close()

	size := info.Size()
	if size < 0 {
		return unavailable()
	}
	start, length, status := int64(0), size, http.StatusOK
	contentRange := ""
	fields := r.Header.Values("Range")
	// HEAD always describes the full response. We do not emit any validator,
	// so an If-Range value cannot match and falls back to a complete GET.
	if !head && len(fields) != 0 && len(r.Header.Values("If-Range")) == 0 {
		from, to, ok := byteRange(fields, size)
		if !ok {
			length, status = 0, http.StatusRequestedRangeNotSatisfiable
			contentRange = fmt.Sprintf("bytes */%d", size)
		} else {
			start, length, status = from, to-from+1, http.StatusPartialContent
			contentRange = fmt.Sprintf("bytes %d-%d/%d", from, to, size)
		}
	}
	if !head && length > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return publicError(err)
		}
	}
	if err := r.Context().Err(); err != nil {
		return publicError(err)
	}

	h := w.Header()
	h.Set("Accept-Ranges", "bytes")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Type", mime)
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	if contentRange != "" {
		h.Set("Content-Range", contentRange)
	}
	w.WriteHeader(status)
	if !head && length > 0 {
		copyVideo(r.Context(), w, v, f, length)
	}
	// A transfer error after headers have been written must never produce an
	// API error body. A short transfer is detectable via Content-Length.
	return nil
}

func copyVideo(ctx context.Context, w http.ResponseWriter, v volume, src io.Reader, remaining int64) {
	controller := http.NewResponseController(w)
	buf := make([]byte, min(streamChunk, remaining))
	for remaining > 0 && ctx.Err() == nil {
		n, readErr := src.Read(buf[:min(int64(len(buf)), remaining)])
		if n <= 0 || ctx.Err() != nil {
			return
		}
		if err := controller.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return
		}
		// If a ResponseWriter wrapper has no deadline support, still release
		// the SMB transport when an HTTP write stalls.
		timer := time.AfterFunc(writeTimeout, v.Close)
		written, err := w.Write(buf[:n])
		timely := timer.Stop()
		_ = controller.SetWriteDeadline(time.Time{})
		if !timely || err != nil || written != n || readErr != nil {
			return
		}
		remaining -= int64(n)
	}
}
