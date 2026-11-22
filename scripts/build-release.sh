#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
version=${1:-}
if [[ $# -ne 1 || ! $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo '用法：bash scripts/build-release.sh v1.0.0' >&2
    exit 2
fi
if ! command -v go >/dev/null && [[ -x .tools/go/bin/go ]]; then
    export PATH="$PWD/.tools/go/bin:$PATH"
fi
out="dist/$version"
mkdir -p -- "$out"
for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
        -ldflags="-s -w -X main.buildVersion=$version" \
        -o "$out/simplewebplayer-linux-$arch" .
done
sed "s/@RELEASE_TAG@/$version/g" install.sh > "$out/install.sh"
chmod 0755 "$out/install.sh"
cp deploy/simplewebplayer.service "$out/simplewebplayer.service"
# Distribute the licenses for the bundled upstream SMB code together with binaries.
cp third_party/go-smb2/LICENSE "$out/go-smb2-LICENSE.txt"
(
    cd "$out"
    sha256sum install.sh simplewebplayer.service go-smb2-LICENSE.txt \
        simplewebplayer-linux-amd64 simplewebplayer-linux-arm64 > SHA256SUMS
)
printf 'Release assets: %s\n' "$out"
