#!/bin/sh
# Install tether on macOS or Linux:
#   curl -fsSL https://raw.githubusercontent.com/gurmukhnishansingh-quilr/quilr-tether/main/install.sh | sh
# Options (environment variables):
#   TETHER_VERSION      tag to install, e.g. v0.1.0 (default: latest release)
#   TETHER_INSTALL_DIR  target directory (default: /usr/local/bin if writable, else ~/.local/bin)
set -eu

REPO="gurmukhnishansingh-quilr/quilr-tether"

say() { printf '%s\n' "$*" >&2; }
die() { say "error: $*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

need curl
need tar
need uname

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported OS $(uname -s); on Windows use install.ps1 or winget" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac

tag="${TETHER_VERSION:-}"
if [ -z "$tag" ]; then
  tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  [ -n "$tag" ] || die "could not find the latest release of $REPO"
fi
version="${tag#v}"
file="tether_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading tether $tag for $os/$arch..."
curl -fsSL "$base/$file" -o "$tmp/$file" || die "download failed: $base/$file"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || die "download failed: checksums.txt"

expected=$(grep " $file\$" "$tmp/checksums.txt" | cut -d ' ' -f 1)
[ -n "$expected" ] || die "$file is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$file" | cut -d ' ' -f 1)
else
  actual=$(shasum -a 256 "$tmp/$file" | cut -d ' ' -f 1)
fi
[ "$expected" = "$actual" ] || die "checksum mismatch for $file"

tar -xzf "$tmp/$file" -C "$tmp" tether

dir="${TETHER_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir"
install -m 755 "$tmp/tether" "$dir/tether" 2>/dev/null || {
  cp "$tmp/tether" "$dir/tether" && chmod 755 "$dir/tether"
}
if [ "$os" = darwin ]; then
  xattr -d com.apple.quarantine "$dir/tether" 2>/dev/null || true
fi

say "Installed tether $version to $dir/tether"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) say "Add $dir to your PATH, e.g.: echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile" ;;
esac
say "Get started: tether profile add <name> --type quilr --region auto"
