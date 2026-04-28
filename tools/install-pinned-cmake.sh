#!/usr/bin/env bash
# Idempotent installer for the cmake version this repo pins. The
# orchestrator's defaultPlatform asserts a specific cmake version and
# the worker image (deploy/buildbarn/runner/Dockerfile) installs that
# same pin. Local dev + CI hosts often ship a different system cmake
# (ubuntu-24.04 ships 3.31.6 today); using the system one means
# converter behavior on cmake 3.31+ slips past local dev and only
# fires in CI. Run this script in CI before any cmake-using e2e and
# in local dev before `make e2e-*` so what you test is what we ship.
#
# Pin lives in the Makefile (CMAKE_VERSION). This script reads from
# there so the pin has one source of truth.
#
# Usage:
#   tools/install-pinned-cmake.sh                       # install to ~/.local/bin
#   PREFIX=/usr/local tools/install-pinned-cmake.sh     # install to /usr/local/bin (needs sudo)
#   CMAKE_VERSION=3.29.2 tools/install-pinned-cmake.sh  # override pin (rare)

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Read the pin from the Makefile if not overridden. The Makefile uses
# `CMAKE_VERSION ?= 3.28.3`; awk picks the literal default, ignoring
# whatever an outer make invocation might shove into env.
default_version="$(sed -n 's/^CMAKE_VERSION[[:space:]]*?=[[:space:]]*\([^[:space:]]*\).*/\1/p' "${repo_root}/Makefile" | head -1)"
VERSION="${CMAKE_VERSION:-${default_version}}"
if [ -z "${VERSION}" ]; then
  echo "install-pinned-cmake: could not read CMAKE_VERSION from Makefile" >&2
  exit 1
fi

PREFIX="${PREFIX:-${HOME}/.local}"
BIN_DIR="${PREFIX}/bin"
OPT_DIR="${PREFIX}/opt"

case "$(uname -s)" in
  Linux)  os=linux ;;
  *) echo "install-pinned-cmake: unsupported OS $(uname -s) (only linux tarballs published)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=x86_64 ;;
  aarch64|arm64) arch=aarch64 ;;
  *) echo "install-pinned-cmake: unsupported arch $(uname -m)" >&2; exit 1 ;;
esac

stamp="${OPT_DIR}/cmake-${VERSION}/.installed"
cmake_bin="${BIN_DIR}/cmake"

if [ -f "${stamp}" ] && [ -x "${cmake_bin}" ]; then
  installed_version="$("${cmake_bin}" --version 2>/dev/null | awk 'NR==1 {print $3}')"
  if [ "${installed_version}" = "${VERSION}" ]; then
    echo "install-pinned-cmake: ${cmake_bin} already at ${VERSION}; nothing to do."
    exit 0
  fi
fi

url="https://github.com/Kitware/CMake/releases/download/v${VERSION}/cmake-${VERSION}-${os}-${arch}.tar.gz"
echo "install-pinned-cmake: fetching ${url}"

mkdir -p "${OPT_DIR}" "${BIN_DIR}"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

curl -L --fail --silent --show-error -o "${tmp}/cmake.tgz" "${url}"
tar -xzf "${tmp}/cmake.tgz" -C "${tmp}"

src="${tmp}/cmake-${VERSION}-${os}-${arch}"
dst="${OPT_DIR}/cmake-${VERSION}"
rm -rf "${dst}"
mv "${src}" "${dst}"
touch "${stamp}"

# Symlink the user-facing binaries into PREFIX/bin. cmake / ctest /
# cpack are all the test paths actually use.
for tool in cmake ctest cpack; do
  ln -sf "${dst}/bin/${tool}" "${BIN_DIR}/${tool}"
done

echo "install-pinned-cmake: installed ${dst} (symlinks in ${BIN_DIR})"
case ":${PATH}:" in
  *":${BIN_DIR}:"*) ;;
  *) echo "install-pinned-cmake: NOTE: ${BIN_DIR} is not on PATH — add it to your shell rc, or PREFIX=/usr/local with sudo." >&2 ;;
esac
