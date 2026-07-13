#!/usr/bin/env bash

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output="${1:-${root}/dist/desktop}"
mkdir -p "${output}"
output="$(cd "${output}" && pwd)"
stage="$(mktemp -d)"
trap 'rm -rf "${stage}"' EXIT
go_cache="${GOCACHE:-${TMPDIR:-/tmp}/lbry-desktop-go-build}"
go_tmp="${GOTMPDIR:-${TMPDIR:-/tmp}}"
mkdir -p "${go_cache}"

build_archive() {
  local goos="$1"
  local archive_os="$2"
  local executable="lbrynet"
  if [[ "${goos}" == "windows" ]]; then
    executable="lbrynet.exe"
  fi
  local directory="${stage}/${archive_os}"
  mkdir -p "${directory}"
  (
    cd "${root}"
    CGO_ENABLED=0 GOOS="${goos}" GOARCH=amd64 GOTOOLCHAIN=local \
      GOCACHE="${go_cache}" GOTMPDIR="${go_tmp}" \
      go build -trimpath -ldflags="-s -w" -o "${directory}/${executable}" .
  )
  chmod 0755 "${directory}/${executable}"
  (
    cd "${directory}"
    zip -q -X "${output}/lbrynet-${archive_os}.zip" "${executable}"
  )
}

build_archive linux linux
build_archive darwin mac
build_archive windows windows

(
  cd "${output}"
  sha256sum lbrynet-linux.zip lbrynet-mac.zip lbrynet-windows.zip > SHA256SUMS
)

printf 'Desktop daemon archives written to %s\n' "${output}"
