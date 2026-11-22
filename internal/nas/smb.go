package nas

import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cloudsoda/go-smb2"

	"simplewebplayer/internal/media"
)

const (
	setupTimeout = 10 * time.Second
	smbTimeout   = 8 * time.Second
)

type smbVolume struct {
	ctx        context.Context
	cancel     context.CancelFunc
	stopSocket func() bool
	socket     net.Conn
	share      *smb2.Share
}

func uncHost(host string) string {
	if ip := net.ParseIP(host); ip != nil && strings.Contains(host, ":") {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
		// Windows UNC uses the IPv6-literal convention, while TCP uses the bare IP.
		return strings.ReplaceAll(ip.String(), ":", "-") + ".ipv6-literal.net"
	}
	return host
}

func dialSMB(parent context.Context, cfg media.Settings) (volume, error) {
	invalid := problem(http.StatusBadRequest, "invalid_settings", "NAS 连接设置无效")
	if err := media.ValidateSettings(cfg); err != nil || strings.EqualFold(cfg.Share, "IPC$") {
		return nil, invalid
	}
	domain, user := cfg.Domain, cfg.Username
	if prefix, rest, ok := strings.Cut(user, `\`); ok {
		if prefix == "" || strings.TrimSpace(rest) == "" || strings.ContainsAny(prefix, `/@:`) || strings.ContainsAny(rest, `\/`) {
			return nil, invalid
		}
		domain, user = prefix, rest
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	setup, stopSetup := context.WithTimeout(parent, setupTimeout)
	defer stopSetup()
	socket, err := (&net.Dialer{}).DialContext(setup, "tcp", addr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	v := &smbVolume{ctx: ctx, cancel: cancel, socket: socket}
	v.stopSocket = context.AfterFunc(ctx, func() { _ = socket.Close() })
	stopAbort := context.AfterFunc(setup, cancel)
	defer stopAbort()
	ready := false
	defer func() {
		if !ready {
			v.Close()
		}
	}()

	dialer := smb2.Dialer{
		Negotiator: smb2.Negotiator{RequireMessageSigning: true},
		Initiator:  &smb2.NTLMInitiator{User: user, Domain: domain, Password: cfg.Password},
	}
	session, err := dialer.DialConn(setup, socket, addr)
	if err != nil {
		return nil, err
	}
	// Mount's short form includes the TCP port in the UNC server name.
	share, err := session.WithContext(setup).Mount(`\\` + uncHost(cfg.Host) + `\` + cfg.Share)
	if err != nil {
		return nil, err
	}
	if share.IsDFS() {
		return nil, unsafePath()
	}
	if err := setup.Err(); err != nil {
		return nil, err
	}
	v.share = share.WithContext(ctx)
	ready = true
	return v, nil
}

func (v *smbVolume) Close() {
	// The private transport owns the NAS session/tree and any unclosed handles.
	// Close TCP directly even when a request is cancelled and SMB CLOSE fails.
	v.stopSocket()
	v.cancel()
	_ = v.socket.Close()
}

// Files capture their Share's context. Cancel the private session on any
// operation timeout, which unblocks credit waits as well as network reads.
// Reset the timer per operation so lengthy but progressing streams can finish.
func smbOp[T any](v *smbVolume, op func() (T, error)) (T, error) {
	var zero T
	if err := v.ctx.Err(); err != nil {
		return zero, err
	}
	timer := time.AfterFunc(smbTimeout, v.cancel)
	defer timer.Stop()
	result, err := op()
	if !timer.Stop() {
		return zero, context.DeadlineExceeded
	}
	if err := v.ctx.Err(); err != nil {
		return zero, err
	}
	return result, err
}

func (v *smbVolume) Open(path string) (file, error) {
	f, err := smbOp(v, func() (*smb2.File, error) {
		return v.share.OpenNoFollow(strings.ReplaceAll(path, "/", `\`))
	})
	if err != nil {
		return nil, err
	}
	return &smbFile{File: f, volume: v}, nil
}

type smbFile struct {
	*smb2.File
	volume *smbVolume
}

func (f *smbFile) Stat() (os.FileInfo, error) {
	return smbOp(f.volume, f.File.Stat)
}

func (f *smbFile) Read(p []byte) (int, error) {
	return smbOp(f.volume, func() (int, error) { return f.File.Read(p) })
}

func (f *smbFile) Readdir(n int) ([]os.FileInfo, error) {
	return smbOp(f.volume, func() ([]os.FileInfo, error) { return f.File.Readdir(n) })
}

func (f *smbFile) Close() error {
	_, err := smbOp(f.volume, func() (struct{}, error) { return struct{}{}, f.File.Close() })
	return err
}
