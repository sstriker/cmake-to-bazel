#!/usr/bin/env bash
# Idempotent local-dev installer for bazelisk. The bazel-tagged e2e tests
# (e2e-bazel-build, e2e-fidelity, e2e-fidelity-fmt) self-skip when bazel
# is not on PATH; running this once gets them out of skip-mode.
#
# Pinned bazelisk version matches .github/workflows/ci.yml so local
# dev hits the same bazel resolution behavior CI does.
#
# Usage:
#   tools/install-bazelisk.sh                # install to ~/.local/bin
#   PREFIX=/usr/local tools/install-bazelisk.sh   # install to /usr/local/bin (needs sudo)

set -euo pipefail

VERSION="${BAZELISK_VERSION:-v1.20.0}"
PREFIX="${PREFIX:-${HOME}/.local}"
BIN_DIR="${PREFIX}/bin"

case "$(uname -s)" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *) echo "install-bazelisk: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "install-bazelisk: unsupported arch $(uname -m)" >&2; exit 1 ;;
esac

dst="${BIN_DIR}/bazelisk"
url="https://github.com/bazelbuild/bazelisk/releases/download/${VERSION}/bazelisk-${os}-${arch}"

if [ -x "${dst}" ]; then
  installed_version="$("${dst}" version 2>/dev/null | awk '/Bazelisk version:/ {print $3}')"
  if [ "${installed_version}" = "${VERSION}" ]; then
    echo "install-bazelisk: ${dst} already at ${VERSION}; nothing to do."
    exit 0
  fi
  echo "install-bazelisk: replacing ${dst} (${installed_version} -> ${VERSION})"
fi

mkdir -p "${BIN_DIR}"
echo "install-bazelisk: fetching ${url}"
tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT
curl -L --fail --silent --show-error -o "${tmp}" "${url}"
install -m 0755 "${tmp}" "${dst}"

# Symlink as `bazel` so test code's exec.LookPath("bazel") resolves
# (the orchestrator's bazelbuild_test + fidelity_e2e_test both look
# for bazel by name).
ln -sf "${dst}" "${BIN_DIR}/bazel"

echo "install-bazelisk: installed ${dst} (and bazel symlink)"
case ":${PATH}:" in
  *":${BIN_DIR}:"*) ;;
  *) echo "install-bazelisk: NOTE: ${BIN_DIR} is not on PATH — add it to your shell rc to use bazelisk by name." >&2 ;;
esac
