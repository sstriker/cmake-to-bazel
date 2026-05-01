# Trace-driven kind:autotools (B → A feedback)

## Why

`kind:cmake` consumes cmake's File API codemodel — a structured
description of what cmake *would* build. Autotools has no such
introspection surface; the conversion has to derive Bazel
targets from what `make` *actually does*.

The B→A feedback loop:

1. **Round 1** — write-a renders the kind:autotools element as
   the existing coarse `install_tree.tar` genrule, with a
   process tracer wrapping the `make`/`make install` invocation.
2. **Bazel build of project B** runs the tracer-wrapped build.
   The tracer captures every `execve` (compile / archive / link
   / install / codegen) under the build sandbox.
3. **Trace registration**: the tracer-wrapped action's outputs
   include both `install_tree.tar` AND `trace.log`. The trace
   gets registered in a CAS-backed index keyed by the element's
   srckey (the same content-addressed key the FUSE-sources
   layer already uses for source serving).
4. **Round 2** — write-a's render checks the index for the
   element's srckey. **Cache hit** → the per-element BUILD
   becomes a `convert-element-autotools` genrule that takes
   the cached trace as input and emits a `BUILD.bazel.out`
   with native `cc_library` / `cc_binary` targets, just like
   `kind:cmake`. **Cache miss** → falls back to the round-1
   coarse genrule.

Monotone: once a trace is registered for a srckey, every
subsequent A render uses it. New source → new srckey → trace
miss → coarse fallback → tracer runs → new trace → new index
entry. The system is self-healing across distributed builders
because the srckey → trace mapping is content-addressed.

## Architecture parallels

The shape mirrors three pieces of existing infrastructure:

- **Read-paths feedback** (`<element>.read-paths.txt`) — the
  precedent for "feedback file alters write-a's render". B→A
  trace generalizes this: same shape (per-element
  feedback-file-influences-render), richer payload (full
  process tree vs. file-read patterns).
- **`@src_<key>//:tree` cas-fuse** — the precedent for
  srckey-keyed CAS lookup. B→A trace adds a parallel index:
  `(srckey, tracer_version) → trace_digest`.
- **kind:cmake's bundle-tar via `cmake_config_bundle`
  filegroup** — the precedent for "B's per-element output
  becomes A's cross-element input on the next round".

## Wishlist: RBE service-managed tracing

The DIY tracer wrapper is fine for v1 but adds:
- A privileged process tracer (`strace` / `bwrap` / a small
  ptrace Go binary) inside the action sandbox.
- An action-output side channel (`trace.log` alongside
  `install_tree.tar`).
- Trust that the tracer's output is deterministic across
  re-runs of the same action.

If buildbarn / buildgrid / EngFlow / similar RBE services
expose process tracing as a server-side feature, we'd:
- Drop the in-action tracer wrapper.
- Receive the trace as a standardized RBE side channel
  (an `Action.tracing_uri`-shaped field on the response or
  similar).
- Inherit the RBE service's determinism / sandbox semantics
  rather than implementing them ourselves.

This is parked as a wishlist item for the RBE community —
trace export from the executor would benefit anyone doing
build introspection of opaque tools, not just our converter.

## Spike status (`cmd/convert-element-autotools/`)

What works today:

- **Parser**: strace text-format input (`-f -e trace=execve
  -s 4096 -o <path>`); recognizes top-level compiler driver
  invocations (cc / gcc / g++ / clang) and filters out
  gcc-internal `cc1` / `as` / `collect2` / `ld` sub-process
  noise.
- **Emitter**: single-step compile-and-link invocations
  (both srcs and `-o <output>`) become `cc_binary`. Cross-
  event correlation (compile-only `cc -c x.c -o x.o` paired
  with archive `ar rcs libfoo.a x.o` paired with link
  `cc -o app app.c -lfoo`) — see follow-ups below.
- **End-to-end fixture**:
  `scripts/spike-autotools-trace.sh` against
  `testdata/meta-project/autotools-greet/sources/`. Builds
  under strace, parses, emits
  `cc_binary(name="greet", srcs=["greet.c"], copts=["-O2"])`.

## Follow-ups

In rough priority order:

1. Cross-event correlation: pair compile-only / archive /
   link events into `cc_library` (for archives) +
   `cc_binary` (for binaries) targets with proper
   `srcs` / `deps`.
2. Cross-element dep resolution: link command's `-l<lib>` /
   `/opt/prefix/lib/lib<X>.so` references → Bazel labels
   via the existing `imports` manifest (mirrors cmake's
   STATIC IMPORTED dep recovery).
3. **Tracer wrapper at action time**: a small Go binary
   that wraps the build invocation and emits a deterministic
   trace artifact. Runs inside Bazel's genrule sandbox.
4. **Trace registry**: REAPI Action Cache entry under
   `(srckey, tracer_version)`. write-a's render gates on
   the AC lookup.
5. **Native handler**: split write-a's `kind:autotools`
   handler into a coarse-fallback path + a native-render
   path; AC hit/miss decides.
