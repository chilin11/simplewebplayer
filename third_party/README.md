# Bundled SMB dependency

`go-smb2/` is the production source from `github.com/cloudsoda/go-smb2` at `v0.0.0-20261105190815-95be04a983b8` (Go module verified before copying). See its `LICENSE` for the upstream BSD license. Upstream tests and GitHub metadata are omitted.

The only source changes are in `client.go`: `Share.OpenNoFollow` requests `FILE_OPEN_REPARSE_POINT`, disables client-side link redirection, and omits `FILE_SHARE_DELETE`; `Share.IsDFS` exposes the server's DFS flags. The player checks the result on each segment and holds the ancestor handles while reading. Reapply these checks (or use equivalent upstream APIs) when upgrading.
