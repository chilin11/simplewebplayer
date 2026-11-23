package nas

import (
	"context"
	"errors"
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

type smbTransport struct {
	ctx        context.Context
	cancel     context.CancelFunc
	stopSocket func() bool
	socket     net.Conn
}

type smbVolume struct {
	*smbTransport
	share *smb2.Share
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

func invalidSMBSettings() *media.Error {
	return problem(http.StatusBadRequest, "invalid_settings", "NAS 连接设置无效")
}

func dialSMB(parent context.Context, cfg media.Settings) (volume, error) {
	if err := media.ValidateSettings(cfg); err != nil {
		return nil, invalidSMBSettings()
	}
	setup, stopSetup := context.WithTimeout(parent, setupTimeout)
	defer stopSetup()
	transport, session, err := dialSMBSession(parent, setup, cfg)
	if err != nil {
		return nil, err
	}
	stopAbort := context.AfterFunc(setup, transport.cancel)
	defer stopAbort()
	share, err := session.WithContext(setup).Mount(cfg.Share)
	if err != nil {
		transport.Close()
		return nil, smbMountError(err)
	}
	if share.IsDFS() {
		transport.Close()
		return nil, unsafePath()
	}
	if setup.Err() != nil {
		transport.Close()
		return nil, nasUnreachable()
	}
	return &smbVolume{smbTransport: transport, share: share.WithContext(transport.ctx)}, nil
}

// dialSMBSession shares the signed NTLM setup between media access and discovery.
// Only the caller's lifetime, not the handshake deadline, owns a ready session.
func dialSMBSession(parent, setup context.Context, cfg media.Settings) (*smbTransport, *smb2.Session, error) {
	domain, user := cfg.Domain, cfg.Username
	if prefix, rest, ok := strings.Cut(user, `\`); ok {
		if prefix == "" || strings.TrimSpace(rest) == "" || strings.ContainsAny(prefix, `/@:`) || strings.ContainsAny(rest, `\/`) {
			return nil, nil, invalidSMBSettings()
		}
		domain, user = prefix, rest
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	socket, err := (&net.Dialer{}).DialContext(setup, "tcp", addr)
	if err != nil {
		return nil, nil, nasUnreachable()
	}
	ctx, cancel := context.WithCancel(parent)
	t := &smbTransport{ctx: ctx, cancel: cancel, socket: socket}
	t.stopSocket = context.AfterFunc(ctx, func() { _ = socket.Close() })
	stopAbort := context.AfterFunc(setup, cancel)
	defer stopAbort()

	dialer := smb2.Dialer{
		Negotiator: smb2.Negotiator{RequireMessageSigning: true},
		Initiator:  &smb2.NTLMInitiator{User: user, Domain: domain, Password: cfg.Password},
	}
	// DialConn uses this argument in UNC tree paths, including ListShares' IPC$.
	session, err := dialer.DialConn(setup, socket, uncHost(cfg.Host))
	if err != nil {
		t.Close()
		return nil, nil, smbSessionError(err)
	}
	if setup.Err() != nil {
		t.Close()
		return nil, nil, nasUnreachable()
	}
	return t, session.WithContext(ctx), nil
}

func (v *smbTransport) Close() {
	// The private transport owns the NAS session/tree and any unclosed handles.
	// Close TCP directly even when a request is cancelled and SMB CLOSE fails.
	v.stopSocket()
	v.cancel()
	_ = v.socket.Close()
}

func smbTransportFailed(err error) bool {
	var transport *smb2.TransportError
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &transport)
}

// Files capture their Share's context. Cancel the private session on any
// operation timeout, which unblocks credit waits as well as network reads.
// Reset the timer per operation so lengthy but progressing streams can finish.
func smbOp[T any](v *smbTransport, op func() (T, error)) (T, error) {
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
	f, err := smbOp(v.smbTransport, func() (*smb2.File, error) {
		return v.share.OpenNoFollow(strings.ReplaceAll(path, "/", `\`))
	})
	if err != nil {
		return nil, err
	}
	return &smbFile{File: f, transport: v.smbTransport}, nil
}

type smbFile struct {
	*smb2.File
	transport *smbTransport
}

func (f *smbFile) Stat() (os.FileInfo, error) {
	return smbOp(f.transport, f.File.Stat)
}

func (f *smbFile) Read(p []byte) (int, error) {
	return smbOp(f.transport, func() (int, error) { return f.File.Read(p) })
}

func (f *smbFile) Readdir(n int) ([]os.FileInfo, error) {
	return smbOp(f.transport, func() ([]os.FileInfo, error) { return f.File.Readdir(n) })
}

func (f *smbFile) Close() error {
	_, err := smbOp(f.transport, func() (struct{}, error) { return struct{}{}, f.File.Close() })
	return err
}
