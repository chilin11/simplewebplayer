#!/usr/bin/env bash
# This source installer is stamped with an immutable release tag by build-release.sh.
set -Eeuo pipefail
export PATH=/usr/sbin:/usr/bin:/sbin:/bin

RELEASE_TAG='@RELEASE_TAG@'
REPO='chilin11/simplewebplayer'
SERVICE=simplewebplayer
DATA=/var/lib/simplewebplayer
CONF=/etc/simplewebplayer/player.env
UNIT=/etc/systemd/system/simplewebplayer.service
BIN=/usr/local/bin/simplewebplayer
work=''
staged=''

die() { printf '安装失败：%s\n' "$*" >&2; exit 1; }
step() { printf '\n[家庭片库] %s\n' "$*"; }
cleanup() {
    if [[ -n $staged ]]; then rm -f -- "$staged"; fi
    if [[ -n $work ]]; then rm -rf -- "$work"; fi
}

fetch() {
    local url=$1 out=$2
    if command -v curl >/dev/null; then
        curl --proto '=https' --proto-redir '=https' -fLsS --connect-timeout 10 --max-time 180 "$url" -o "$out"
    else
        wget --https-only --timeout=30 --tries=2 -q -O "$out" "$url"
    fi
}

verified() {
    local file=$1 want
    want=$(awk -v name="$file" '$2 == name {print $1}' "$work/SHA256SUMS")
    [[ $want =~ ^[[:xdigit:]]{64}$ ]] || die "$file 的校验记录无效。"
    (cd "$work" && printf '%s  %s\n' "$want" "$file" | sha256sum --check --status) || die "$file 的校验失败，未安装。"
}

lan_host() {
    local addr=''
    if command -v ip >/dev/null; then
        addr=$(ip -4 route get 1.1.1.1 2>/dev/null | awk 'NR==1 {for(i=1;i<NF;i++) if($i=="src"){print $(i+1); break}}' || true)
    fi
    case "$addr" in
        10.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[01].*) printf '%s\n' "$addr" ;;
        *) printf '127.0.0.1\n' ;;
    esac
}

configured() {
    local key=$1 default=$2 val
    val=$(awk -F= -v k="$key" '$1 == k {sub(/^[^=]*=/, ""); v=$0} END {print v}' "$CONF")
    printf '%s\n' "${val:-$default}"
}

reachable() {
    local url=$1
    if command -v curl >/dev/null; then
        curl -fSs --noproxy '*' --max-time 2 -o /dev/null "$url" 2>/dev/null
    else
        wget --no-proxy --timeout=2 --tries=1 -q -O /dev/null "$url"
    fi
}

main() {
    [[ $# -eq 0 ]] || die '这个安装脚本不接受参数；监听地址可在安装后调整。'
    [[ $RELEASE_TAG =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die '请使用 GitHub Release 的 install.sh，不要从源码分支下载。'
    [[ $(uname -s) == Linux && -d /run/systemd/system ]] || die '需要使用 systemd 启动的 Linux。'
    [[ $EUID -eq 0 ]] || die '需要管理员权限；请通过 sudo bash 运行。'
    command -v curl >/dev/null || command -v wget >/dev/null || die '请在系统上安装 curl 或 wget。'
    local cmd arch artifact base host port url shown i
    for cmd in awk flock getent groupadd install mktemp mv runuser sha256sum systemctl useradd; do
        command -v "$cmd" >/dev/null || die "系统缺少 $cmd。"
    done
    case $(uname -m) in
        x86_64|amd64) arch=amd64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) die '目前只提供 x86_64 和 ARM64 安装包。' ;;
    esac
    exec 9>/run/simplewebplayer-install.lock
    flock -n 9 || die '另一个安装任务正在运行。'
    trap cleanup EXIT
    work=$(mktemp -d)
    artifact="simplewebplayer-linux-$arch"
    base="https://github.com/$REPO/releases/download/$RELEASE_TAG"
    step "下载 $RELEASE_TAG（Linux $arch）并校验文件"
    for cmd in SHA256SUMS "$artifact" simplewebplayer.service; do
        fetch "$base/$cmd" "$work/$cmd" || die "下载 $cmd 失败。"
    done
    verified "$artifact"
    verified simplewebplayer.service

    getent group "$SERVICE" >/dev/null || groupadd --system "$SERVICE"
    if ! getent passwd "$SERVICE" >/dev/null; then
        local nologin
        nologin=$(command -v nologin) || die '系统缺少 nologin。'
        useradd --system --gid "$SERVICE" --home-dir "$DATA" --no-create-home --shell "$nologin" "$SERVICE"
    fi
    [[ $(id -u "$SERVICE") -ne 0 ]] || die '服务账号不能是 root。'
    install -d -m 0700 -o "$SERVICE" -g "$SERVICE" "$DATA"
    install -d -m 0755 /etc/simplewebplayer
    [[ -d /usr/local/bin ]] || install -d -m 0755 /usr/local/bin
    if [[ ! -e $CONF ]]; then
        host=$(lan_host)
        umask 077
        printf 'PLAYER_HOST=%s\nPLAYER_PORT=8080\n' "$host" > "$CONF"
    fi
    host=$(configured PLAYER_HOST 127.0.0.1)
    port=$(configured PLAYER_PORT 8080)
    case "$host" in
        0.0.0.0|::) shown=127.0.0.1 ;;
        *:*) shown="[$host]" ;;
        *) shown=$host ;;
    esac
    url="http://$shown:$port/"

    # Stage under a root-managed directory; the service user cannot traverse work.
    staged=$(mktemp /usr/local/bin/.simplewebplayer.XXXXXX)
    install -m 0755 "$work/$artifact" "$staged"
    [[ $(runuser -u "$SERVICE" -- "$staged" --version) == "$RELEASE_TAG" ]] || die '二进制版本与下载版本不一致。'
    step '检查初始管理员（已有账号和授权将保留）'
    if { : </dev/tty; } 2>/dev/null; then
        runuser -u "$SERVICE" -- env PLAYER_DATA_DIR="$DATA" "$staged" init-admin --username admin --if-needed </dev/tty
    else
        runuser -u "$SERVICE" -- env PLAYER_DATA_DIR="$DATA" "$staged" init-admin --username admin --if-needed </dev/null \
            || die '首次安装需在交互终端中创建管理员；SSH 请使用交互式终端。'
    fi
    mv -fT -- "$staged" "$BIN"
    staged=''
    install -m 0644 "$work/simplewebplayer.service" "$UNIT"

    step '启动服务并设为开机运行'
    systemctl daemon-reload
    systemctl enable --quiet "$SERVICE.service"
    systemctl restart "$SERVICE.service"
    for ((i = 0; i < 8; i++)); do
        if systemctl is-active --quiet "$SERVICE.service" && reachable "${url}api/session"; then
            printf '\n安装完成：%s\n登录后，在管理台配置 NAS、创建用户和分配目录。\n' "$url"
            return 0
        fi
        sleep 1
    done
    die '服务未通过启动检查，请运行 sudo journalctl -u simplewebplayer -n 50 查看原因。'
}

main "$@"
