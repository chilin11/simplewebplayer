// Package media defines the shared contract between the HTTP API and SMB adapter.
package media

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Settings struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Share    string `json:"share"`
	Username string `json:"username"`
	Domain   string `json:"domain"`
	Password string `json:"-"`
}

type Entry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Size *int64 `json:"size"`
}

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Backend never receives an absolute SMB path. Base is relative to the SMB
// share, and path is relative to base. Stream returns errors only before it
// writes status or bytes; the caller serializes them as API errors.
type Backend interface {
	Test(ctx context.Context, settings Settings) error
	List(ctx context.Context, settings Settings, base, path string) ([]Entry, error)
	Stream(w http.ResponseWriter, r *http.Request, settings Settings, base, path string) error
}

// ShareBrowser is an optional capability used by the admin setup form. It
// never persists settings. Names are unescaped top-level SMB disk shares.
type ShareBrowser interface {
	DiscoverShares(context.Context, Settings) ([]string, error)
}

// NormalizePath validates without trimming or resolving path segments: the empty
// string alone means the root. Leading spaces and Unicode whitespace are legal.
func NormalizePath(path string) (string, error) {
	invalid := &Error{Status: http.StatusBadRequest, Code: "invalid_path", Message: "目录路径无效"}
	if len(path) > 4096 || !utf8.ValidString(path) {
		return "", invalid
	}
	if path == "" {
		return "", nil
	}
	parts := strings.Split(path, "/")
	if len(parts) > 64 {
		return "", invalid
	}
	for _, part := range parts {
		if !validSegment(part) {
			return "", invalid
		}
	}
	return path, nil
}

var hostLabel = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func validSegment(s string) bool {
	if s == "" || !utf8.ValidString(s) || strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") || strings.ContainsAny(s, `\/:*?"<>|`) {
		return false
	}
	units := 0
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
		units++
		if r > 0xffff {
			units++
		}
	}
	return units <= 255
}

// ValidateSettings accepts a bare NAS host and a single top-level share.
func ValidateSettings(s Settings) error {
	if !IsShareName(s.Share) {
		return errors.New("请选择有效的 NAS 共享文件夹")
	}
	return ValidateServer(s)
}

// IsShareName accepts one share segment, not a filesystem path or IPC$.
func IsShareName(s string) bool { return validSegment(s) && !strings.EqualFold(s, "IPC$") }

// ValidateServer performs validation that does not depend on a selected share.
// Password is validated separately by the API so rotation can omit the field.
func ValidateServer(s Settings) error {
	if len(s.Host) == 0 || len(s.Host) > 253 || s.Port < 1 || s.Port > 65535 {
		return errors.New("请填写有效的 NAS 地址和端口")
	}
	if net.ParseIP(s.Host) == nil {
		for _, label := range strings.Split(strings.TrimSuffix(s.Host, "."), ".") {
			if !hostLabel.MatchString(label) {
				return errors.New("NAS 地址必须是主机名或裸 IP 地址")
			}
		}
	}
	if strings.TrimSpace(s.Username) == "" || len(s.Username) > 256 || len(s.Domain) > 255 || strings.ContainsAny(s.Domain, `\/@:`) ||
		strings.IndexFunc(s.Username+s.Domain, unicode.IsControl) >= 0 || !utf8.ValidString(s.Username+s.Domain) {
		return errors.New("NAS 用户名或域无效")
	}
	return nil
}
