// Package nas implements the read-only Go SMB media backend.
package nas

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/cloudsoda/go-smb2"

	"simplewebplayer/internal/media"
)

const (
	maxStreams                = 12
	maxEntries                = 5000
	dirBatch                  = 128
	listTimeout               = 30 * time.Second
	fileAttributeReparsePoint = 0x400
)

var videoMIME = map[string]string{
	".mp4":  "video/mp4",
	".m4v":  "video/x-m4v",
	".webm": "video/webm",
	".ogv":  "video/ogg",
	".mov":  "video/quicktime",
	".mkv":  "video/x-matroska",
}

type volume interface {
	// Open must not follow links and must not share delete access.
	Open(string) (file, error)
	Close()
}

type file interface {
	io.ReadSeekCloser
	Stat() (os.FileInfo, error)
	Readdir(int) ([]os.FileInfo, error)
}

// Backend is safe for concurrent requests. Each request opens its own session.
type Backend struct {
	streams chan struct{}
	connect func(context.Context, media.Settings) (volume, error)
}

func New() *Backend {
	return &Backend{streams: make(chan struct{}, maxStreams), connect: dialSMB}
}

var _ media.Backend = (*Backend)(nil)

func problem(status int, code, message string) *media.Error {
	return &media.Error{Status: status, Code: code, Message: message}
}

func unavailable() *media.Error {
	return problem(http.StatusBadGateway, "nas_unavailable", "无法访问 NAS")
}

func notFound() *media.Error {
	return problem(http.StatusNotFound, "not_found", "找不到 NAS 文件或目录")
}

func unsafePath() *media.Error {
	return problem(http.StatusForbidden, "unsafe_path", "不支持 NAS 链接、重解析点或 DFS 路径")
}

func accessDenied() *media.Error {
	return problem(http.StatusForbidden, "nas_access_denied", "NAS 认证失败或拒绝访问")
}

func publicError(err error) error {
	var known *media.Error
	if errors.As(err, &known) {
		return known
	}
	if errors.Is(err, os.ErrPermission) {
		return accessDenied()
	}
	if errors.Is(err, os.ErrNotExist) {
		return notFound()
	}
	var response *smb2.ResponseError
	if errors.As(err, &response) {
		switch response.Code {
		case 0x8000002d, 0xc0000257, 0xc000026d: // STOPPED_ON_SYMLINK, PATH_NOT_COVERED, DFS_UNAVAILABLE
			return unsafePath()
		case 0xc000006a, 0xc000006d, 0xc0000071: // WRONG_PASSWORD, LOGON_FAILURE, PASSWORD_EXPIRED
			return accessDenied()
		case 0xc000000f, 0xc00000ba, 0xc00000cc, 0xc0000103: // NO_SUCH_FILE, FILE_IS_A_DIRECTORY, BAD_NETWORK_NAME, NOT_A_DIRECTORY
			return notFound()
		}
	}
	return unavailable()
}

func join(base, name string) string {
	if base == "" {
		return name
	}
	if name == "" {
		return base
	}
	return base + "/" + name
}

func targetPath(base, relative string) (string, error) {
	base, err := media.NormalizePath(base)
	if err != nil {
		return "", err
	}
	relative, err = media.NormalizePath(relative)
	if err != nil {
		return "", err
	}
	return media.NormalizePath(join(base, relative))
}

func reparse(info *smb2.FileStat) bool {
	return info.FileAttributes&fileAttributeReparsePoint != 0 || info.ReparsePointTag != 0
}

func checkInfo(fi os.FileInfo, directory bool) error {
	info, ok := fi.(*smb2.FileStat)
	if !ok || info == nil {
		return unavailable()
	}
	if reparse(info) {
		return unsafePath()
	}
	if (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return notFound()
	}
	return nil
}

type anchoredFile struct {
	file
	parents []file
}

func closeParents(parents []file) {
	for i := len(parents) - 1; i >= 0; i-- {
		_ = parents[i].Close()
	}
}

func (f *anchoredFile) Close() error {
	defer closeParents(f.parents)
	return f.file.Close()
}

// Open each segment without following links. Hold the verified ancestor
// handles without FILE_SHARE_DELETE, so conforming SMB writers cannot replace
// the path while it is in use. The NAS must not follow server-side links or
// permit writes through other protocols that bypass SMB share-mode locks.
func openChecked(v volume, target string, directory bool) (file, os.FileInfo, error) {
	var parts []string
	if target != "" {
		parts = strings.Split(target, "/")
	}
	var parents []file
	defer func() { closeParents(parents) }()
	prefix := ""
	for i := 0; i <= len(parts); i++ {
		if i != 0 {
			prefix = join(prefix, parts[i-1])
		}
		f, err := v.Open(prefix)
		if err != nil {
			return nil, nil, publicError(err)
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, nil, publicError(err)
		}
		last := i == len(parts)
		if err := checkInfo(info, directory || !last); err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		if last {
			result := &anchoredFile{file: f, parents: parents}
			parents = nil
			return result, info, nil
		}
		parents = append(parents, f)
	}
	return nil, nil, unavailable()
}

func (b *Backend) Test(ctx context.Context, cfg media.Settings) error {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	v, err := b.connect(ctx, cfg)
	if err != nil {
		return publicError(err)
	}
	defer v.Close()
	f, _, err := openChecked(v, "", true)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return publicError(err)
	}
	return nil
}

func (b *Backend) List(ctx context.Context, cfg media.Settings, base, relative string) ([]media.Entry, error) {
	target, err := targetPath(base, relative)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	v, err := b.connect(ctx, cfg)
	if err != nil {
		return nil, publicError(err)
	}
	defer v.Close()
	f, _, err := openChecked(v, target, true)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	entries := make([]media.Entry, 0)
	count := 0
	for {
		batch, err := f.Readdir(dirBatch)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, publicError(err)
		}
		for _, fi := range batch {
			info, ok := fi.(*smb2.FileStat)
			if !ok || info == nil {
				return nil, unavailable()
			}
			name := info.Name()
			if name == "." || name == ".." { // Protocol pseudo-entries are not user files.
				continue
			}
			count++
			if count > maxEntries {
				return nil, problem(http.StatusUnprocessableEntity, "directory_too_large", "NAS 目录超过 5000 项")
			}
			if reparse(info) || name == "" || strings.ContainsRune(name, '/') {
				continue
			}
			if _, err := media.NormalizePath(join(target, name)); err != nil {
				continue
			}
			e := media.Entry{Name: name, Path: join(relative, name)}
			if info.IsDir() {
				e.Kind = "directory"
			} else if info.Mode().IsRegular() && videoMIME[strings.ToLower(path.Ext(name))] != "" && info.Size() >= 0 {
				size := info.Size()
				e.Kind, e.Size = "video", &size
			} else {
				continue
			}
			entries = append(entries, e)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if len(batch) == 0 {
			return nil, unavailable()
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Kind != b.Kind {
			return a.Kind == "directory"
		}
		left, right := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if left == right {
			return a.Name < b.Name
		}
		return left < right
	})
	return entries, nil
}
