# Custom bb-runner-bare image with cmake / ninja / bwrap pre-installed at
# the versions the orchestrator's defaultPlatform asserts. Without these
# tools on the runner, REAPI Action submissions of convert-element fail
# at exec time even though the protocol round-trip succeeds.
#
# Strategy: multi-stage. The upstream bb-runner-bare image is typically
# distroless (no apt-get, no shell). We extract the bb-runner-bare binary
# from it and install it onto a Debian base where we can layer the
# CMake-toolchain dependencies via apt.
#
# Versions MUST match deploy/buildbarn/config/worker.jsonnet's platform
# properties and the Makefile's CMAKE_VERSION / NINJA_VERSION /
# BWRAP_VERSION pins. Bumping any pin requires updating all four.
#
# Build:
#   docker build -t cmake-to-bazel/bb-runner:local deploy/buildbarn/runner
#
# Or via docker compose (the bb-runner-bare service references this via
# its build: stanza).

# Stage 1: pull the upstream binary out of the pinned image.
FROM ghcr.io/buildbarn/bb-runner-bare:20251031T081715Z-4415657 AS upstream

# Stage 2: Debian base with the toolchain tools installed.
FROM debian:bookworm-slim AS runtime

ARG CMAKE_VERSION=3.28.3
ARG NINJA_VERSION=1.11.1
ARG BWRAP_VERSION=0.8.0

# bookworm ships:
#   cmake          3.25.1   (need 3.28.3 — install from kitware apt repo)
#   ninja-build    1.11.1   (matches NINJA_VERSION pin exactly)
#   bubblewrap     0.8.0    (matches BWRAP_VERSION pin exactly)
#
# For cmake 3.28.x we use Kitware's apt repo, which carries the exact
# upstream tarball at this version. Avoids vendoring binaries into the
# image and keeps the version pinned without manual hash management.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates curl gpg \
        ninja-build \
        bubblewrap \
    ; \
    # Install pinned cmake from Kitware's apt repo — bookworm's stock
    # cmake is 3.25.x. Kitware publishes for jammy/noble but the deb
    # works on bookworm too (only depends on libc6 + libssl3).
    install -d -m 0755 /etc/apt/keyrings; \
    curl -fsSL https://apt.kitware.com/keys/kitware-archive-latest.asc \
      | gpg --dearmor -o /etc/apt/keyrings/kitware.gpg; \
    echo "deb [signed-by=/etc/apt/keyrings/kitware.gpg] https://apt.kitware.com/ubuntu/ jammy main" \
      > /etc/apt/sources.list.d/kitware.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends "cmake"; \
    apt-get purge -y --auto-remove curl gpg; \
    rm -rf /var/lib/apt/lists/*; \
    # Hard version-fence: if the installed versions drift from
    # defaultPlatform pins, the build fails here instead of silently
    # making the worker incompatible with the orchestrator at exec time.
    cmake --version | head -n 1 | grep -F "${CMAKE_VERSION}" || (echo "cmake version mismatch (want ${CMAKE_VERSION})" >&2 ; exit 1); \
    ninja --version | grep -F "${NINJA_VERSION}" || (echo "ninja version mismatch (want ${NINJA_VERSION})" >&2 ; exit 1); \
    bwrap --version | grep -F "${BWRAP_VERSION}" || (echo "bwrap version mismatch (want ${BWRAP_VERSION})" >&2 ; exit 1)

# bb-runner-bare's binary + entrypoint config from the upstream layer.
# Path inside the upstream image is the conventional Buildbarn install
# location; if upstream changes layout, this COPY needs adjustment.
COPY --from=upstream /bb-runner-bare /bb-runner-bare

ENTRYPOINT ["/bb-runner-bare"]
