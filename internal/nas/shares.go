package nas

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/cloudsoda/go-smb2"
	"simplewebplayer/internal/media"
)

var _ media.ShareBrowser = (*Backend)(nil)

func nasUnreachable() *media.Error {
	return problem(http.StatusBadGateway, "nas_unreachable", "无法连到 NAS。请检查 NAS 地址、SMB 服务和端口（默认 445），并确认播放器服务器可以访问 NAS。")
}

func smbSessionError(err error) *media.Error {
	if smbTransportFailed(err) {
		return nasUnreachable()
	}
	var res *smb2.ResponseError
	var invalid *smb2.InvalidResponseError
	denied := errors.Is(err, os.ErrPermission)
	if errors.As(err, &res) {
		switch res.Code {
		case 0xc0000022, 0xc0000064, 0xc000006a, 0xc000006d, 0xc000006e, 0xc000006f, 0xc0000070, 0xc0000071, 0xc0000072, 0xc0000193, 0xc0000224, 0xc0000234:
			denied = true
		}
	}
	if errors.As(err, &invalid) && (invalid.Message == "guest account doesn't support signing" || invalid.Message == "anonymous account doesn't support signing") {
		denied = true
	}
	if denied {
		return problem(http.StatusForbidden, "nas_auth_failed", "NAS 未接受该账号。请确认使用 DSM 本地用户（不是 Synology Account），密码正确，且该用户获准使用 SMB。可在 Finder 用同一账号连接 NAS 对照。")
	}
	return problem(http.StatusBadGateway, "nas_protocol_error", "无法建立 SMB 会话。请在 NAS 开启 SMB2/SMB3 并确认支持 SMB 签名，不要为排查启用 SMB1。")
}

func smbMountError(err error) error {
	var res *smb2.ResponseError
	if errors.Is(err, os.ErrNotExist) || (errors.As(err, &res) && res.Code == 0xc00000cc) {
		return problem(http.StatusNotFound, "nas_share_not_found", "NAS 找不到这个共享文件夹。请在 DSM「控制面板 → 共享文件夹」确认顶层名称，不要填 NAS 文件系统路径。")
	}
	return publicError(err)
}

const maxShareCandidates = 64

func shareCandidates(items []smb2.ShareInfo) ([]string, error) {
	if len(items) > 512 {
		return nil, tooManyShares()
	}
	names := make([]string, 0)
	seen := make(map[string]bool)
	for _, item := range items {
		name := item.Name
		if item.Type() != smb2.ShareTypeDiskTree || item.IsSpecial() || strings.HasSuffix(name, "$") || !media.IsShareName(name) {
			continue
		}
		key := strings.ToLower(name)
		if !seen[key] {
			seen[key] = true
			names = append(names, name)
			if len(names) > maxShareCandidates {
				return nil, tooManyShares()
			}
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	return names, nil
}

func tooManyShares() *media.Error {
	return problem(http.StatusUnprocessableEntity, "nas_shares_unavailable", "NAS 返回的共享文件夹过多。请从 DSM「控制面板 → 共享文件夹」选择名称后手动填写。")
}

func (b *Backend) DiscoverShares(ctx context.Context, cfg media.Settings) ([]string, error) {
	if err := media.ValidateServer(cfg); err != nil || cfg.Password == "" {
		return nil, invalidSMBSettings()
	}
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	t, session, err := dialSMBSession(ctx, ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer t.Close()
	return discoverReadableShares(ctx, session.ListShares, func(name string) error {
		return probeShare(t, session, name)
	})
}

func discoverReadableShares(ctx context.Context, list func() ([]smb2.ShareInfo, error), probe func(string) error) ([]string, error) {
	items, err := list()
	if ctx.Err() != nil || smbTransportFailed(err) {
		return nil, nasUnreachable()
	}
	if err != nil {
		return nil, problem(http.StatusBadGateway, "nas_shares_unavailable", "NAS 未能列出共享文件夹。你可以在 DSM 确认名称，然后手动填写并测试该共享的权限。")
	}
	names, err := shareCandidates(items)
	if err != nil {
		return nil, err
	}
	readable := make([]string, 0, len(names))
	for _, name := range names {
		if ctx.Err() != nil {
			return nil, nasUnreachable()
		}
		err := probe(name)
		if err == nil {
			readable = append(readable, name)
			continue
		}
		var public *media.Error
		if !errors.As(err, &public) || (public.Status != http.StatusForbidden && public.Status != http.StatusNotFound) {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, nasUnreachable()
	}
	return readable, nil
}

func probeShare(t *smbTransport, session *smb2.Session, name string) (probeErr error) {
	sh, err := session.Mount(name)
	if err != nil {
		return smbMountError(err)
	}
	sh = sh.WithContext(t.ctx)
	defer func() {
		// A dead session must not look like an empty (or partial) share list.
		if smbTransportFailed(sh.Umount()) {
			probeErr = nasUnreachable()
		}
	}()
	if sh.IsDFS() {
		return unsafePath()
	}
	f, _, err := openChecked(&smbVolume{smbTransport: t, share: sh}, "", true)
	if err != nil {
		return err
	}
	return publicErrorIfAny(f.Close())
}

func publicErrorIfAny(err error) error {
	if err == nil {
		return nil
	}
	return publicError(err)
}
