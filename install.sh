#!/bin/sh

# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 alibaba/open-code-review Contributors

# Install the ocr (Open Code Review) CLI and the ocr_ext companion binary
# from GitHub releases.
#   curl -fsSL https://open-codereview.ai/install.sh | sh
# Prefer to inspect first:
#   curl -fsSL https://open-codereview.ai/install.sh -o install.sh
#   less install.sh && sh install.sh
# Env: OCR_INSTALL_DIR (default /usr/local/bin), OCR_VERSION (default latest),
# OCR_GITHUB_MIRROR (default unset; download the binary through a mirror domain).
set -eu

main() {
  REPO="alibaba/open-code-review"
  BIN="ocr"
  ASSET_PREFIX="opencodereview"
  EXT_BIN="ocr_ext"
  EXT_ASSET_PREFIX="ocrext"
  INSTALL_DIR="${OCR_INSTALL_DIR:-/usr/local/bin}"
  VERSION="${OCR_VERSION:-}"

  command -v curl >/dev/null 2>&1 || err "curl is required"

  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$os" in
    darwin|linux) ;;
    *) err "unsupported OS: $os (on Windows use: irm https://open-codereview.ai/install.ps1 | iex)" ;;
  esac

  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) err "unsupported architecture: $arch" ;;
  esac

  if [ -z "$VERSION" ]; then
    release_json="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest")" ||
      err "failed to fetch latest release info from github api"
    VERSION="$(printf '%s' "$release_json" |
      sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
    [ -n "$VERSION" ] || err "could not resolve latest release tag"
  fi

  prefix="$(printf '%s' "${OCR_GITHUB_MIRROR:-}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
  prefix="${prefix#https://}"
  prefix="${prefix#http://}"
  prefix="${prefix%/}"
  case "$prefix" in *[[:space:]]*) err "OCR_GITHUB_MIRROR contains spaces: '$prefix'" ;; esac
  if [ -n "$prefix" ]; then
    printf 'warning: downloading from unofficial GitHub mirror "%s" (checksum integrity is not guaranteed)\n' "$prefix" >&2
    base="https://${prefix}/github.com/$REPO/releases/download/$VERSION"
  else
    base="https://github.com/$REPO/releases/download/$VERSION"
  fi
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' INT TERM EXIT

  curl -fsSL --connect-timeout 5 --max-time 15 -o "$tmp/sha256sum.txt" "$base/sha256sum.txt" ||
    err "sha256sum.txt download failed"

  # Fetch and verify both assets before installing either, so a failed run
  # cannot leave the two binaries on different versions. The companion is
  # best-effort: releases older than ocr_ext carry no asset or checksum
  # entry for it.
  fetch_asset "${ASSET_PREFIX}-${os}-${arch}" ||
    err "download failed: ${ASSET_PREFIX}-${os}-${arch}"
  if fetch_asset "${EXT_ASSET_PREFIX}-${os}-${arch}"; then
    INSTALL_EXT=1
  else
    printf 'note: %s not found in this release; only %s is updated\n' "$EXT_BIN" "$BIN"
    INSTALL_EXT=0
  fi

  install_binary "$tmp/${ASSET_PREFIX}-${os}-${arch}" "$INSTALL_DIR" "$BIN"
  printf 'installed %s %s -> %s\n' "$BIN" "$VERSION" "$INSTALL_DIR/$BIN"
  if [ "$INSTALL_EXT" = 1 ]; then
    install_binary "$tmp/${EXT_ASSET_PREFIX}-${os}-${arch}" "$INSTALL_DIR" "$EXT_BIN"
    printf 'installed %s %s -> %s\n' "$EXT_BIN" "$VERSION" "$INSTALL_DIR/$EXT_BIN"
  fi

  post_install_path_notice "$BIN" "$INSTALL_DIR"
}

# fetch_asset downloads one release asset and verifies it against the
# already fetched sha256sum.txt. Returns non-zero on any failure; the caller
# decides whether that is fatal.
fetch_asset() {
  asset="$1"

  printf 'downloading %s (%s/%s)...\n' "$asset" "$os" "$arch"
  curl -fL --progress-bar --connect-timeout 5 --speed-limit 1024 --speed-time 30 -o "$tmp/$asset" "$base/$asset" ||
    return 1

  want="$(awk -v a="$asset" '$2 == a {print tolower($1)}' "$tmp/sha256sum.txt")"
  [ -n "$want" ] || return 1
  got="$(sha256 "$tmp/$asset" | awk '{print tolower($1)}')"
  [ "$got" = "$want" ] || return 1
}

# Install the staged binary (mode 0755), escalating with sudo only when needed.
# Using install(1) under sudo gives the binary root ownership in system dirs.
install_binary() {
  src="$1"
  dir="$2"
  bin="$3"
  if mkdir -p "$dir" 2>/dev/null && [ -w "$dir" ]; then
    install -m 0755 "$src" "$dir/$bin"
  elif command -v sudo >/dev/null 2>&1; then
    printf 'note: %s is not writable; escalating with sudo\n' "$dir"
    sudo mkdir -p "$dir"
    sudo install -m 0755 "$src" "$dir/$bin"
  else
    err "$dir is not writable and sudo is unavailable; set OCR_INSTALL_DIR to a writable path"
  fi
}

post_install_path_notice() {
  bin="$1"
  install_dir="$2"
  case ":$PATH:" in
    *":$install_dir:"*) ;;
    *) printf 'note: %s is not on your PATH; add it or run %s/%s directly\n' "$install_dir" "$install_dir" "$bin"; return ;;
  esac
  command -v "$bin" >/dev/null 2>&1 || printf 'note: open a new shell so %s resolves on PATH\n' "$bin"
}

# Print the SHA-256 of a file, preferring shasum (macOS) over sha256sum (Linux).
sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1"
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1"
  else
    err "shasum or sha256sum is required for checksum verification"
  fi
}

err() { printf 'error: %s\n' "$1" >&2; exit 1; }

main "$@"
