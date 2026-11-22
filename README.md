# SimpleWebPlayer · 家庭 NAS 片库

面向家庭或小团队的局域网视频在线播放系统。Go 后端自带网页，直接通过 SMB 2/3 读 NAS，不需要 Python、Node、数据库服务或 Linux 文件系统挂载。网站账号和 NAS 的只读服务账号独立。

## 一条命令安装

在 **Linux（systemd，x86_64 或 ARM64）** 的交互终端运行：

```sh
wget -qO- https://github.com/chilin11/simplewebplayer/releases/latest/download/install.sh | sudo bash
```

安装脚本会下载并校验 [GitHub Release](https://github.com/chilin11/simplewebplayer/releases/latest) 的 Go 二进制，创建低权限服务账号，启动服务并设置开机运行。首次安装时设置 `admin` 的密码（不会回显，没有默认密码）；安装结束会打印访问地址。默认选择检测到的局域网地址，检测不到则只监听 `127.0.0.1`，端口为 `8080`。

**升级**：重跑同一条命令，保留 `/var/lib/simplewebplayer` 中的账号、NAS 配置及授权；也保留 `/etc/simplewebplayer/player.env`。如果需要调整监听地址或端口，修改这个配置文件的 `PLAYER_HOST` / `PLAYER_PORT`，然后执行 `sudo systemctl restart simplewebplayer`。局域网 HTTP 没有传输加密，只在可信网络使用。

<details>
<summary>手动运行 / 从源码构建</summary>

用 Go 1.26 或更高版本构建（运行机器无需 Go）。若已下载二进制，把以下命令中的 `./simplewebplayer` 换成下载的文件名并授予执行权限。

```sh
CGO_ENABLED=0 go build -trimpath -o ./simplewebplayer .
export PLAYER_DATA_DIR="$PWD/data"
./simplewebplayer init-admin --username admin
./simplewebplayer serve --host 127.0.0.1 --port 8080
```

初始化在交互终端中隐藏密码输入，没有默认密码。先创建初始管理员，再启动服务。只允许远程 NAS 在局域网内访问；无需在 NAS 或路由器上把 TCP 445 暴露到互联网。

- **同机访问：** 打开 `http://127.0.0.1:8080`。
- **局域网访问：** 将 `--host` 换成服务器的局域网 IP，例如 `192.168.1.201`，浏览器访问 `http://192.168.1.201:8080`；防火墙仅放行可信局域网。HTTP 没有传输加密，不要在不可信网络使用。
</details>

## 配置 NAS 和授权

1. 在 NAS 上创建专用 SMB **只读**账号。共享内只放可供这个应用管理的媒体，不要使用 NAS 管理员账号。关闭 SMB1，使用直连 SMB 共享，不使用 DFS、不允许服务端跟随链接（例如 Samba 配置 `follow symlinks = no`、`wide links = no`）。
2. 登录网站，打开管理台 → **NAS 连接**，填写 NAS 主机或 IP（不是 URL）、TCP 端口（通常 445）、共享名称、用户名及密码，必要时填写域。保存后点测试连接。密码输入留空表示保留已保存密码，已存密码不会回显。
3. 在 **授权目录** 浏览挑选路径并设置显示名称，例如 `Kids` 或 `Movies/Documentaries`。路径相对于 SMB 共享；只有真正的空路径才表示授权整个共享，通常应选具体文件夹。根路径建立后不可改；换路径请新建根、分配授权并移除旧根。NAS 地址/共享/账号等身份变化也需先移除旧根，避免旧权限指向新来源。
4. 在 **账号与权限** 新建普通账号，在该账号的授权列表勾选允许播放的目录。普通账号只能看到被授予的目录及后代；管理员可以看到全部已配置的目录。禁用、改密、降权或删除会撤销对应会话；撤回目录权限会阻止下一次浏览、播放或下载，已下载的文件无法收回，当前已打开的流可能继续至断开。

**目录隔离前提：** 只用可信、只读媒体共享，禁用会跟随的符号链接、junction、DFS，并用 NAS ACL 限制可写者。应用不会跟随重解析点或 DFS，播放期间保持 SMB 祖先目录句柄以防遵守 SMB 共享锁的客户端同时替换路径；它不能管住 NAS 管理员、本地写入或绕过 SMB 锁的其他协议。不要把任意不可信用户可改写的共享目录当作租户隔离边界；需要这种隔离时，应在 NAS 上使用不同共享和独立 ACL/部署实例。

## 播放范围

浏览器直接播放，支持 HTTP Range 拖动进度，不转码。片库和播放页可下载授权范围内的原视频。优先使用 **MP4（H.264 + AAC）**，或浏览器支持的 WebM。列表也识别 M4V、OGV、MOV、MKV，但容器或编码不兼容时无法播放；尤其 MKV/部分 HEVC 不通用。只读访问，不提供上传、转码、刮削、字幕管理或公网分享链接。

## 以后接 cloudflared

在 `/etc/simplewebplayer/player.env` 设置 `PLAYER_HOST=127.0.0.1`，在同一 Linux 上将 Cloudflare Tunnel 的源站设为 `http://127.0.0.1:8080`。公网只通过 HTTPS 访问；在同一配置文件设置 `PLAYER_SECURE_COOKIES=1` 并重启（此模式不能直接在局域网普通 HTTP 上登录）。推荐先启用 **Cloudflare Access** 白名单/身份认证及边缘登录限速。应用不信任任意转发头；隧道回源时由边缘识别并限制实际客户端。确认 Tunnel/Access 配置允许视频 Range，不缓存 `/api/*`，并按计划/条款确认大流量媒体的使用限制。不要同时在路由器转发应用端口或 SMB 445。

## 数据、维护与开源参考

- 一键安装默认使用 `/var/lib/simplewebplayer`（手动启动默认 `./data`）。持久化备份应在停服后同时复制 `player.db` 和 `player.key`（保留私有权限）。仅恢复数据库而不带匹配密钥，服务不会启动；两者都不可分享。NAS 密码在库内用 AES-GCM 加密，网站密码采用加盐 scrypt。
- 技术栈：Go `net/http`、SQLite（`modernc.org/sqlite`）、[go-smb2](https://github.com/cloudsoda/go-smb2)、HTML 原生播放器。借鉴 [Jellyfin](https://github.com/jellyfin/jellyfin) 的媒体库/用户权限划分思路，没有引入其媒体服务器或前端框架。[第三方 SMB 源码及修改说明](third_party/README.md) 随项目提供。
- 维护者可用 `bash scripts/build-release.sh vX.Y.Z` 在本机生成 Linux x86_64 / ARM64 二进制、安装脚本、服务文件及校验清单，再把 `dist/vX.Y.Z/` 的文件上传至相同版本的 GitHub Release。项目不会触发 GitHub Actions 构建。
