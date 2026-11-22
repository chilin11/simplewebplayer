package nas

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cloudsoda/go-smb2"

	"simplewebplayer/internal/media"
)

const directoryAttribute = 0x10

func info(name string, attrs uint32, size int64) *smb2.FileStat {
	return &smb2.FileStat{FileName: name, FileAttributes: attrs, EndOfFile: size}
}

type stubFile struct {
	*strings.Reader
	info    os.FileInfo
	entries []os.FileInfo
	closed  bool
	reads   int
	onRead  func()
}

func (f *stubFile) Stat() (os.FileInfo, error) { return f.info, nil }
func (f *stubFile) Close() error               { f.closed = true; return nil }
func (f *stubFile) Read(p []byte) (int, error) {
	f.reads++
	if f.onRead != nil {
		f.onRead()
	}
	return f.Reader.Read(p)
}
func (f *stubFile) Readdir(n int) ([]os.FileInfo, error) {
	if len(f.entries) == 0 {
		return nil, io.EOF
	}
	n = min(n, len(f.entries))
	batch := f.entries[:n]
	f.entries = f.entries[n:]
	return batch, nil
}

type stubVolume struct {
	stats   map[string]os.FileInfo
	paths   []string
	handle  *stubFile
	target  string
	parents []*stubFile
	closed  bool
	statErr error
}

func (v *stubVolume) Open(p string) (file, error) {
	v.paths = append(v.paths, p)
	if v.statErr != nil {
		return nil, v.statErr
	}
	for _, f := range v.parents {
		if f.closed {
			return nil, errors.New("parent unpinned before child open")
		}
	}
	fi, ok := v.stats[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	if p == v.target {
		return v.handle, nil
	}
	f := &stubFile{Reader: strings.NewReader(""), info: fi}
	v.parents = append(v.parents, f)
	return f, nil
}
func (v *stubVolume) Close() { v.closed = true }

func fakeBackend(target string, fi os.FileInfo, body string) (*Backend, *stubVolume) {
	v := &stubVolume{
		stats:  map[string]os.FileInfo{"": info("", directoryAttribute, 0), "base": info("base", directoryAttribute, 0), target: fi},
		handle: &stubFile{Reader: strings.NewReader(body), info: fi},
		target: target,
	}
	b := New()
	b.connect = func(context.Context, media.Settings) (volume, error) { return v, nil }
	return b, v
}

func errorCode(err error) string {
	var problem *media.Error
	if errors.As(err, &problem) {
		return problem.Code
	}
	return ""
}

func TestListReturnsBaseRelativeEntries(t *testing.T) {
	b, v := fakeBackend("base/library", info("library", directoryAttribute, 0), "")
	v.handle.entries = []os.FileInfo{
		info("clip.MKV", 0, 19), info("sub", directoryAttribute, 0), info("note.txt", 0, 1),
		info("junction", directoryAttribute|fileAttributeReparsePoint, 0), info("link.mp4", fileAttributeReparsePoint, 1),
		info("../out.mp4", 0, 1), info(`name\escape.mp4`, 0, 1), info("..", directoryAttribute, 0),
	}
	entries, err := b.List(t.Context(), media.Settings{}, "base", "library")
	if err != nil {
		t.Fatal(err)
	}
	size := int64(19)
	want := []media.Entry{
		{Name: "sub", Path: "library/sub", Kind: "directory"},
		{Name: "clip.MKV", Path: "library/clip.MKV", Kind: "video", Size: &size},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %#v", entries)
	}
	if !reflect.DeepEqual(v.paths, []string{"", "base", "base/library"}) || !v.closed || !v.handle.closed {
		t.Fatalf("paths = %v, file closed=%v, volume closed=%v", v.paths, v.handle.closed, v.closed)
	}
	for _, parent := range v.parents {
		if !parent.closed {
			t.Fatal("verified ancestor handle not closed")
		}
	}
}

func TestProbeOpensRootWithoutListingIt(t *testing.T) {
	b, v := fakeBackend("", info("", directoryAttribute, 0), "")
	v.handle.entries = []os.FileInfo{info("untouched.mp4", 0, 10)}
	if err := b.Test(t.Context(), media.Settings{}); err != nil {
		t.Fatal(err)
	}
	if !v.handle.closed || !v.closed || len(v.handle.entries) != 1 {
		t.Fatal("probe enumerated the directory or leaked its root handle")
	}
}

func TestListTooLargeDoesNotReturnPartialResults(t *testing.T) {
	b, v := fakeBackend("base", info("base", directoryAttribute, 0), "")
	for range maxEntries + 1 {
		v.handle.entries = append(v.handle.entries, info("filtered.txt", 0, 0))
	}
	entries, err := b.List(t.Context(), media.Settings{}, "base", "")
	if errorCode(err) != "directory_too_large" || entries != nil || !v.closed || !v.handle.closed {
		t.Fatalf("partial results=%v, error=%v, closed=%t/%t", entries != nil, err, v.closed, v.handle.closed)
	}
}

func TestPathChecksStopBeforeReading(t *testing.T) {
	for _, openedReparse := range []bool{false, true} {
		b, v := fakeBackend("base/movie.mp4", info("movie.mp4", 0, 4), "data")
		if openedReparse {
			v.handle.info = info("movie.mp4", fileAttributeReparsePoint, 4)
		} else {
			v.stats["base"] = info("base", directoryAttribute|fileAttributeReparsePoint, 0)
		}
		w := httptest.NewRecorder()
		err := b.Stream(w, httptest.NewRequest("GET", "/", nil), media.Settings{}, "base", "movie.mp4")
		if errorCode(err) != "unsafe_path" || !v.closed || v.handle.reads != 0 || w.Body.Len() != 0 {
			t.Fatalf("reparse handle=%v, error=%v, closed=%t, reads=%d", openedReparse, err, v.closed, v.handle.reads)
		}
		if openedReparse && !v.handle.closed {
			t.Fatal("unsafe handle was not closed")
		}
		if !openedReparse && !reflect.DeepEqual(v.paths, []string{"", "base"}) {
			t.Fatal("descended below unsafe prefix")
		}
	}
	b, v := fakeBackend("base/movie.mp4", info("movie.mp4", 0, 4), "data")
	if _, err := b.List(t.Context(), media.Settings{}, "base/..", ""); errorCode(err) != "invalid_path" || len(v.paths) != 0 {
		t.Fatalf("invalid path: %v (Lstat %v)", err, v.paths)
	}
}

func TestStreamRangeResponses(t *testing.T) {
	for _, tc := range []struct {
		name, method, ifRange, data string
		fields                      []string
		status, length              int
		contentRange, body          string
	}{
		{"full", "GET", "", "0123456789", nil, 200, 10, "", "0123456789"},
		{"closed", "GET", "", "0123456789", []string{"bytes=2-4"}, 206, 3, "bytes 2-4/10", "234"},
		{"open", "GET", "", "0123456789", []string{"bytes=8-"}, 206, 2, "bytes 8-9/10", "89"},
		{"suffix", "GET", "", "0123456789", []string{"bytes=-2"}, 206, 2, "bytes 8-9/10", "89"},
		{"large suffix", "GET", "", "0123456789", []string{"bytes=-99999999999999999999999999"}, 206, 10, "bytes 0-9/10", "0123456789"},
		{"out of range", "GET", "", "0123456789", []string{"bytes=10-"}, 416, 0, "bytes */10", ""},
		{"multipart", "GET", "", "0123456789", []string{"bytes=1-2,4-5"}, 416, 0, "bytes */10", ""},
		{"duplicate", "GET", "", "0123456789", []string{"bytes=1-2", "bytes=4-5"}, 416, 0, "bytes */10", ""},
		{"invalid", "GET", "", "0123456789", []string{"bytes=3-1"}, 416, 0, "bytes */10", ""},
		{"if range", "GET", `"old"`, "0123456789", []string{"bytes=1-2"}, 200, 10, "", "0123456789"},
		{"HEAD full length", "HEAD", "", "0123456789", []string{"bytes=1-2"}, 200, 10, "", ""},
		{"empty", "GET", "", "", nil, 200, 0, "", ""},
		{"empty range", "GET", "", "", []string{"bytes=0-"}, 416, 0, "bytes */0", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, v := fakeBackend("base/movie.MP4", info("movie.MP4", 0, int64(len(tc.data))), tc.data)
			w, r := httptest.NewRecorder(), httptest.NewRequest(tc.method, "/", nil)
			for _, field := range tc.fields {
				r.Header.Add("Range", field)
			}
			if tc.ifRange != "" {
				r.Header.Set("If-Range", tc.ifRange)
			}
			if err := b.Stream(w, r, media.Settings{}, "base", "movie.MP4"); err != nil {
				t.Fatal(err)
			}
			h := w.Result().Header
			if w.Code != tc.status || w.Body.String() != tc.body || h.Get("Content-Length") != strconv.Itoa(tc.length) || h.Get("Content-Range") != tc.contentRange {
				t.Fatalf("status=%d, range=%q, length=%q, body=%q", w.Code, h.Get("Content-Range"), h.Get("Content-Length"), w.Body.String())
			}
			if h.Get("Content-Type") != "video/mp4" || h.Get("Cache-Control") != "no-store" || h.Get("Accept-Ranges") != "bytes" {
				t.Fatalf("unexpected headers: %v", h)
			}
			if !v.closed || !v.handle.closed || (tc.body == "" && v.handle.reads != 0) || len(b.streams) != 0 {
				t.Fatal("unreleased resources or unexpected SMB read")
			}
		})
	}
}

func TestStreamSlotsDoNotBlockList(t *testing.T) {
	b, v := fakeBackend("base", info("base", directoryAttribute, 0), "")
	for range maxStreams {
		b.streams <- struct{}{}
	}
	w := httptest.NewRecorder()
	err := b.Stream(w, httptest.NewRequest("GET", "/", nil), media.Settings{}, "base", "movie.mp4")
	var p *media.Error
	if !errors.As(err, &p) || p.Status != http.StatusServiceUnavailable || len(v.paths) != 0 {
		t.Fatalf("stream limit error=%v, paths=%v", err, v.paths)
	}
	if _, err := b.List(t.Context(), media.Settings{}, "base", ""); err != nil {
		t.Fatalf("listing was blocked: %v", err)
	}
}

type brokenWriter struct{ *httptest.ResponseRecorder }

func (w brokenWriter) Write([]byte) (int, error) { return 0, errors.New("client write failed") }

func TestStreamAbortAfterCommitReleasesResources(t *testing.T) {
	for _, cancelDuringRead := range []bool{false, true} {
		b, v := fakeBackend("base/movie.mp4", info("movie.mp4", 0, 4), "data")
		ctx, cancel := context.WithCancel(t.Context())
		w := brokenWriter{httptest.NewRecorder()}
		if cancelDuringRead {
			v.handle.onRead = cancel
		}
		err := b.Stream(w, httptest.NewRequestWithContext(ctx, "GET", "/", nil), media.Settings{}, "base", "movie.mp4")
		cancel()
		if err != nil || w.Code != 200 || !v.closed || !v.handle.closed || len(b.streams) != 0 {
			t.Fatalf("abort error=%v status=%d closed=%v/%v", err, w.Code, v.closed, v.handle.closed)
		}
	}
}

func TestSMBErrorsDoNotExposeBackendDetails(t *testing.T) {
	b, v := fakeBackend("base/movie.mp4", info("movie.mp4", 0, 4), "data")
	v.statErr = &os.PathError{Op: "open", Path: "nas/private.mp4", Err: errors.New("hidden password")}
	err := b.Stream(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), media.Settings{}, "base", "movie.mp4")
	if errorCode(err) != "nas_unavailable" || strings.Contains(err.Error(), "private.mp4") || strings.Contains(err.Error(), "password") || !v.closed {
		t.Fatalf("error=%v, closed=%v", err, v.closed)
	}
	if errorCode(publicError(&smb2.ResponseError{Code: 0xc0000257})) != "unsafe_path" {
		t.Fatal("unexpected DFS response classification")
	}
}
