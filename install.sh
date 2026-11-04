#!/bin/sh
# Installs the latest sal release binary for this machine from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/cgs-earth/sal/main/install.sh | sh
#
# SAL_VERSION=v0.1.2 installs that release instead of the latest, and
# SAL_INSTALL_DIR chooses the directory, ~/.local/bin by default. Releases are
# built for linux/amd64 and darwin/arm64; anything else has to build from
# source with `go install github.com/cgs-earth/sal@latest`.
set -eu

repo="cgs-earth/sal"
install_dir="${SAL_INSTALL_DIR:-$HOME/.local/bin}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in
  x86_64 | amd64) arch="amd64" ;;
  arm64 | aarch64) arch="arm64" ;;
esac
archive="sal_${os}_${arch}.tar.gz"
case "${os}_${arch}" in
  linux_amd64 | darwin_arm64) ;;
  *)
    echo "sal has no prebuilt binary for ${os}/${arch}; build it from source with:" >&2
    echo "  go install github.com/cgs-earth/sal@latest" >&2
    exit 1
    ;;
esac

if [ -n "${SAL_VERSION:-}" ]; then
  base="https://github.com/${repo}/releases/download/${SAL_VERSION}"
else
  base="https://github.com/${repo}/releases/latest/download"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "downloading ${base}/${archive}"
curl -fsSL -o "${tmp}/${archive}" "${base}/${archive}"
curl -fsSL -o "${tmp}/checksums.txt" "${base}/checksums.txt"

# Only the line naming this archive is checked, since the checksum file lists
# every platform's.
cd "$tmp"
if command -v sha256sum >/dev/null 2>&1; then
  grep " ${archive}\$" checksums.txt | sha256sum -c - >/dev/null
else
  grep " ${archive}\$" checksums.txt | shasum -a 256 -c - >/dev/null
fi
tar -xzf "$archive"

mkdir -p "$install_dir"
install -m 0755 sal "${install_dir}/sal"
echo "installed $("${install_dir}/sal" --version) to ${install_dir}/sal"

case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *) echo "add ${install_dir} to your PATH to run sal" ;;
esac
