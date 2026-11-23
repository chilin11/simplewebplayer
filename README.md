# SimpleWebPlayer · 家庭 NAS 片库

面向家庭或小团队的局域网视频在线播放系统。Go 后端自带网页，直接通过 SMB 2/3 读 NAS，不需要 Python、Node、数据库服务或 Linux 文件系统挂载。网站账号和 NAS 的只读服务账号独立。

## 一条命令安装

在 **Linux（systemd，x86_64 或 ARM64）** 的交互终端运行：

```sh
(installer=$(mktemp) && trap 'rm -f -- "$installer"' EXIT && wget -O "$installer" https://github.com/chilin11/simplewebplayer/releases/latest/download/install.sh && sudo bash "$installer")
```

安装脚本会下载并校验 [GitHub Release](https://github.com/chilin11/simplewebplayer/releases/latest) 的 Go 二进制，创建低权限服务账号，启动服务并设置开机运行。首次安装时设置 `admin` 的密码（不会回显，没有默认密码）；安装结束会打印访问地址。默认选择检测到的局域网地址，检测不到则只监听 `127.0.0.1`，端口为 `8080`。

## 一条命令升级

已经用一键脚本安装过的 systemd Linux（包括 v1.0.0 / v1.1.0），用独立的 `upgrade.sh`：

```sh
(upgrade=$(mktemp) && trap 'rm -f -- "$upgrade"' EXIT && wget -qO "$upgrade" https://github.com/chilin11/simplewebplayer/releases/latest/download/upgrade.sh && sudo bash "$upgrade")
```

升级不会重设管理员密码，也不会修改原有账号、NAS、目录授权和监听配置。已是最新版本会直接退出；有新版本时会短暂重启服务。不自动降级，不在启动失败时自动回退。失败时先看 `sudo journalctl -u simplewebplayer -n 50`。需要回退时，应预先停服备份 `/var/lib/simplewebplayer/` 和 `/etc/simplewebplayer/player.env`；从备份成套恢复后，用指定旧标签的 `install.sh` 手动回退。不要删除密钥或重新初始化管理员。

调整监听地址或端口可修改 `/etc/simplewebplayer/player.env` 的 `PLAYER_HOST` / `PLAYER_PORT`，再执行 `sudo systemctl restart simplewebplayer`。局域网 HTTP 只在可信网络使用。

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
2. 登录网站，打开管理台 → **NAS 连接**，填写 NAS 局域网 IP 或主机名（不是 DSM 网址）、端口（通常 `445`）、NAS 本地用户名和密码。群晖本地用户的域通常留空。点击 **连接 NAS**，从列表选一个可读取的共享文件夹，再 **保存配置**。如果 NAS 禁止列出共享，可点 **手动填写**：名称就是 DSM「控制面板 → 共享文件夹」中的顶层名称（例如 `video`，不是 `/volume1/video`）。保存后可点击 **测试已保存连接**。连接预览不会保存密码；保存后的密码不会回显。
3. 保存共享后点 **选择授权目录…**（也可从 **管理台 → 权限 → 授权目录 → 浏览 NAS…** 进入），挑选路径并设置显示名称，例如 `Kids` 或 `Movies/Documentaries`。添加后点 **在片库打开** 即可浏览该目录，管理员不需要先给自己授权。路径相对于 SMB 共享；只有真正的空路径才表示授权整个共享，通常应选具体文件夹。根路径建立后不可改；换路径请新建根、分配授权并移除旧根。NAS 地址/共享/账号等身份变化也需先移除旧根，避免旧权限指向新来源。
4. 在同一个 **权限** 页面找到 **账号与授权**，新建普通账号，点击该账号的 **分配目录**，勾选允许播放的目录并保存。普通账号只能看到被分配的目录及后代；管理员默认可看全部已建目录，不需要分配。禁用、改密、降权或删除会撤销对应会话；撤回目录权限会阻止下一次浏览、播放或下载，已下载的文件无法收回，当前已打开的流可能继续至断开。

**保持登录：** 同一设备的同一浏览器登录一次后，服务端不再固定到期，每次打开片库都会续期浏览器 Cookie。旧版本尚有效的登录在升级后打开片库时会自动续期；已经过期的需再登录一次。浏览器清理站点数据、无痕模式、长时间不打开或更换地址/浏览器，都可能需要重登。共享设备用完请点 **退出登录**；改密码、停用或删除账号仍会撤销登录。

**DSM 登录正常但连接失败？** DSM 网页账号能登录不代表它有 SMB 权限。确认「控制面板 → 文件服务 → SMB」已启用并允许 SMB2/SMB3；检查该本地用户的 SMB 应用权限（如 DSM 版本有此项）和目标共享的读取权限。可在同一个局域网用 Mac Finder → `⌘K` 打开 `smb://NAS的IP`，选择「注册用户」并用同一个账号对照测试，避免用 Finder 缓存的另一个账号。没有必要开启 SMB1 或匿名访问。

**目录隔离前提：** 只用可信、只读媒体共享，禁用会跟随的符号链接、junction、DFS，并用 NAS ACL 限制可写者。应用不会跟随重解析点或 DFS，播放期间保持 SMB 祖先目录句柄以防遵守 SMB 共享锁的客户端同时替换路径；它不能管住 NAS 管理员、本地写入或绕过 SMB 锁的其他协议。不要把任意不可信用户可改写的共享目录当作租户隔离边界；需要这种隔离时，应在 NAS 上使用不同共享和独立 ACL/部署实例。

## 播放范围

浏览器直接播放，支持 HTTP Range 拖动进度，不转码。片库和播放页可下载授权范围内的原视频。优先使用 **MP4（H.264 + AAC）**，或浏览器支持的 WebM。列表也识别 M4V、OGV、MOV、MKV，但容器或编码不兼容时无法播放；尤其 MKV/部分 HEVC 不通用。只读访问，不提供上传、转码、刮削、字幕管理或公网分享链接。

## 投屏到电视：Google Cast 与 DLNA

在 **同一局域网** 用 `http://服务器局域网IP:8080` 打开片库，选视频 → **投屏到电视**。列表同时搜索 **Google Cast**（例如内置 Chromecast 的 TCL Google TV）与 **DLNA/UPnP MediaRenderer**（取决于机型和设置，例如部分 LG webOS）。同一台电视可能列出两个带不同协议标签的选项。电视会直接请求有权限的视频，不用在电视登录，也不用安装浏览器插件。

- **找不到电视：** 先唤醒电视，确认与 Linux 在同一 IPv4 网段、不在访客 Wi‑Fi、无线设备隔离已关闭。在电视设置中核对接收器名称，按机型开启 Google Cast、DLNA 或媒体接收后重新搜索。电视待机可能不广播；手机视频 App 可投也不一定使用同一种协议。防火墙如有限制，只在可信局域网允许 mDNS UDP 5353、SSDP UDP 1900、Linux 至电视的控制连接（Google Cast TCP 8009；DLNA 端口由电视宣告），以及电视至应用的 HTTP 端口。不要对公网开放这些端口。
- **找得到但播不了：** 首次请试 **MP4（H.264 + AAC）**。其他格式和续播取决于电视；部分 DLNA 设备会从头播。不转码，不支持投屏 MKV、AirPlay、Miracast 或整屏镜像。
- **只用于家中可信网络：** 服务仅从同网段直连 HTTP 控制投屏，反代/Cloudflare 地址不提供投屏。电视获得绑定到设备 IP、登录会话和文件的临时取流权限，撤销授权、退出或停播后新请求会失败；局域网发现不验证电视的硬件身份。需要禁用功能时在 `/etc/simplewebplayer/player.env` 加 `PLAYER_DISABLE_CAST=1` 并重启。

## 以后接 cloudflared

在 `/etc/simplewebplayer/player.env` 设置 `PLAYER_HOST=127.0.0.1`，在同一 Linux 上将 Cloudflare Tunnel 的源站设为 `http://127.0.0.1:8080`。公网只通过 HTTPS 访问；在同一配置文件设置 `PLAYER_SECURE_COOKIES=1` 并重启（此模式不能直接在局域网普通 HTTP 上登录）。推荐先启用 **Cloudflare Access** 白名单/身份认证及边缘登录限速。应用不信任任意转发头；隧道回源时由边缘识别并限制实际客户端。确认 Tunnel/Access 配置允许视频 Range，不缓存 `/api/*`，并按计划/条款确认大流量媒体的使用限制。不要同时在路由器转发应用端口或 SMB 445。

## 数据、维护与开源参考

- 一键安装默认使用 `/var/lib/simplewebplayer`（手动启动默认 `./data`）。持久化备份应在停服后同时复制 `player.db` 和 `player.key`（保留私有权限）。已有数据库但缺少密钥时，服务会拒绝启动；若误用了另一个实例的有效密钥，服务可能启动，但无法解密已保存的 NAS 密码。因此应成套备份和恢复，两者都不可分享。NAS 密码在库内用 AES-GCM 加密，网站密码采用加盐 scrypt。
- 技术栈：Go `net/http`、SQLite（`modernc.org/sqlite`）、[go-smb2](https://github.com/cloudsoda/go-smb2)、HTML 原生播放器。借鉴 [Jellyfin](https://github.com/jellyfin/jellyfin) 的媒体库/用户权限划分思路，没有引入其媒体服务器或前端框架。[第三方 SMB 源码及修改说明](third_party/README.md) 随项目提供。
- 维护者可用 `bash scripts/build-release.sh vX.Y.Z` 在本机生成 Linux x86_64 / ARM64 二进制、安装脚本、服务文件及校验清单，再把 `dist/vX.Y.Z/` 的文件上传至相同版本的 GitHub Release。项目不会触发 GitHub Actions 构建。
