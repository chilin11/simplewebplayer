package nas

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"testing"

	"github.com/cloudsoda/go-smb2"

	"simplewebplayer/internal/media"
)

func TestSMBShareDiscoveryFilters(t *testing.T) {
	items := []smb2.ShareInfo{
		{Name: "zeta"}, {Name: "ZETA"}, {Name: " 音乐"}, {Name: "Alpha"},
		{Name: "Private"}, {Name: "Gone"}, {Name: "DFS"},
		{Name: "IPC$", TypeFlags: uint32(smb2.ShareTypeIPC)},
		{Name: "Printer", TypeFlags: uint32(smb2.ShareTypePrintQueue)},
		{Name: "Device", TypeFlags: uint32(smb2.ShareTypeDevice)},
		{Name: "Special", TypeFlags: 0x80000000}, // STYPE_SPECIAL disk tree
		{Name: "hidden$"}, {Name: `bad\path`}, {Name: "trailing "}, {Name: ""},
	}
	var probed []string
	got, err := discoverReadableShares(t.Context(), func() ([]smb2.ShareInfo, error) {
		return items, nil
	}, func(name string) error {
		probed = append(probed, name)
		switch name {
		case "Private":
			return smbMountError(os.ErrPermission)
		case "Gone":
			return smbMountError(&smb2.ResponseError{Code: 0xc00000cc})
		case "DFS":
			return unsafePath()
		}
		return nil
	})
	if want := []string{" 音乐", "Alpha", "DFS", "Gone", "Private", "zeta"}; !reflect.DeepEqual(probed, want) {
		t.Fatalf("probed = %q, want %q", probed, want)
	}
	if want := []string{" 音乐", "Alpha", "zeta"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, error %v; want %q", got, err, want)
	}
}

func TestSMBShareDiscoveryFailures(t *testing.T) {
	transport := &os.PathError{Op: "smb", Err: &smb2.TransportError{Err: io.ErrUnexpectedEOF}}
	for _, tt := range []struct {
		name     string
		listErr  error
		probeErr error
		cancelAt string
		wantCode string
	}{
		{name: "IPC permission fallback", listErr: os.ErrPermission, wantCode: "nas_shares_unavailable"},
		{name: "IPC timeout", listErr: context.DeadlineExceeded, wantCode: "nas_unreachable"},
		{name: "IPC transport", listErr: transport, wantCode: "nas_unreachable"},
		{name: "probe transport discards earlier success", probeErr: publicError(transport), wantCode: "nas_unreachable"},
		{name: "canceled after empty enumeration", cancelAt: "list", wantCode: "nas_unreachable"},
		{name: "canceled on final probe", cancelAt: "probe", wantCode: "nas_unreachable"},
		{name: "empty success"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			got, err := discoverReadableShares(ctx, func() ([]smb2.ShareInfo, error) {
				if tt.cancelAt == "list" {
					cancel()
				}
				if tt.probeErr != nil || tt.cancelAt == "probe" {
					return []smb2.ShareInfo{{Name: "First"}, {Name: "Last"}}, nil
				}
				return nil, tt.listErr
			}, func(name string) error {
				if name == "Last" {
					if tt.cancelAt == "probe" {
						cancel()
					}
					return tt.probeErr
				}
				return nil
			})
			if tt.wantCode == "" {
				if err != nil || got == nil || len(got) != 0 {
					t.Fatalf("got %#v, error %v; want a successful empty list", got, err)
				}
				return
			}
			if got != nil {
				t.Fatalf("returned a list on failure: %q", got)
			}
			checkSMBShareProblem(t, err, http.StatusBadGateway, tt.wantCode)
		})
	}
}

func TestSMBShareConnectionErrors(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"session auth", smbSessionError(&smb2.ResponseError{Code: 0xc000006d}), 403, "nas_auth_failed"},
		{"session EOF", smbSessionError(&smb2.TransportError{Err: io.EOF}), 502, "nas_unreachable"},
		{"session protocol", smbSessionError(&smb2.InvalidResponseError{Message: "bad response"}), 502, "nas_protocol_error"},
		{"mount permission", smbMountError(&os.PathError{Err: os.ErrPermission}), 403, "nas_access_denied"},
		{"mount access denied status", smbMountError(&smb2.ResponseError{Code: 0xc0000022}), 403, "nas_access_denied"},
		{"mount missing", smbMountError(&smb2.ResponseError{Code: 0xc00000cc}), 404, "nas_share_not_found"},
		{"mount timeout", smbMountError(context.DeadlineExceeded), 502, "nas_unreachable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkSMBShareProblem(t, tt.err, tt.status, tt.code)
		})
	}
}

func checkSMBShareProblem(t *testing.T, err error, status int, code string) {
	t.Helper()
	var public *media.Error
	if !errors.As(err, &public) || public.Status != status || public.Code != code {
		t.Fatalf("error = %#v, want %d %s", err, status, code)
	}
}
