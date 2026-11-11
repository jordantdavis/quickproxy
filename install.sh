#!/bin/sh
# Install quickproxy from the GitHub Releases of jordantdavis/quickproxy.
#
#   curl -fsSL https://raw.githubusercontent.com/jordantdavis/quickproxy/main/install.sh | sh
#
# Environment:
#   VERSION      release tag to install, for example v1.0.0 (default: latest)
#   INSTALL_DIR  where to put the binary (default: $HOME/.local/bin)
set -eu

REPO="jordantdavis/quickproxy"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${VERSION:-}"

err() {
	echo "install.sh: $*" >&2
	exit 1
}

command -v curl >/dev/null 2>&1 || err "curl is required"
command -v tar >/dev/null 2>&1 || err "tar is required"

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) err "unsupported OS: $(uname -s)" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) err "unsupported architecture: $(uname -m)" ;;
esac

# Resolve "latest" from the redirect of the releases/latest page, which
# avoids the GitHub API rate limit.
if [ -z "$VERSION" ]; then
	url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
		err "could not find the latest release"
	VERSION="${url##*/}"
	case "$VERSION" in
	v*) ;;
	*) err "no release found for $REPO" ;;
	esac
fi

name="quickproxy_${VERSION}_${os}_${arch}"
base="https://github.com/$REPO/releases/download/$VERSION"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading quickproxy $VERSION for $os/$arch"
curl -fsSL -o "$tmp/$name.tar.gz" "$base/$name.tar.gz" ||
	err "download failed: $base/$name.tar.gz"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" ||
	err "download failed: $base/checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
	sha="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
	sha="shasum -a 256"
else
	err "sha256sum or shasum is required"
fi
want=$(awk -v f="$name.tar.gz" '$2 == f { print $1 }' "$tmp/checksums.txt")
got=$($sha "$tmp/$name.tar.gz" | awk '{ print $1 }')
[ -n "$want" ] || err "no checksum for $name.tar.gz in checksums.txt"
[ "$want" = "$got" ] || err "checksum mismatch for $name.tar.gz"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/$name/quickproxy" "$INSTALL_DIR/quickproxy"
echo "Installed $INSTALL_DIR/quickproxy ($("$INSTALL_DIR/quickproxy" -version))"

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*) echo "Note: $INSTALL_DIR is not in your PATH. Add it to your shell profile:
  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
