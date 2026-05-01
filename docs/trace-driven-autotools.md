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

## How it runs (single-genrule flow)

```mermaid
sequenceDiagram
    participant WA as cmd/write-a
    participant Bazel as bazel build (project A)
    participant BT as build-tracer
    participant Build as ./configure + make + make install
    participant CEA as convert-element-autotools

    WA->>WA: Render install genrule with tracer + converter wired in
    Bazel->>BT: $(location //tools:build-tracer) --out=$TRACE -- sh -c '...'
    BT->>Build: fork + ptrace; capture every execve
    Build-->>BT: install_tree at $INSTALL_ROOT
    BT-->>Bazel: trace.log (strace-format)
    Bazel->>CEA: $(location //tools:convert-element-autotools) --trace=$TRACE
    CEA-->>Bazel: BUILD.bazel.out (cc_library / cc_binary)
    Bazel->>Bazel: tar install_tree.tar
    Note over Bazel: Action result cached: srckey + toolchain + tool digests<br/>=> install_tree.tar + BUILD.bazel.out
```

One Bazel action, two outputs. Bazel's action cache (buildbarn
in CI, local cache for dev) handles cross-node convergence — same
source + same toolchain + same converter version => same cache
key => same outputs.

## Component status

- **`cmd/build-tracer`**: native ptrace backend (linux/amd64)
  + strace fallback (`--strace`). Forks the build with
  PTRACE_TRACEME, follows fork/vfork/clone, captures every
  successful execve's argv. Output is strace-compatible text
  format. Runs inside Bazel's standard linux-sandbox; no host
  strace dep on linux/amd64.
- **`cmd/convert-element-autotools`**: parses the trace,
  classifies events into compile / link / archive, builds a
  correlation graph, emits BUILD.bazel.out with cc_library
  (per archive) + cc_binary (per link). `-l<name>` resolves
  to `:<name>` for in-trace archives or to a cross-element
  Bazel label via the imports manifest. Default-toolchain
  flags (`-O2`, `-fPIC`, `-g`, `-DNDEBUG`) stripped;
  `-D<name>=<val>` routes to the rule's `defines`.
- **`cmd/write-a`** (`autotoolsHandler`): when
  `--convert-element-autotools` + `--build-tracer-bin` are
  set, renders the install genrule with the tracer wrap +
  converter step inline. Emits `imports.json` next to the
  BUILD when there are cross-element deps. Without those
  flags, falls back to the unmodified coarse install_tree.tar
  pipeline.
- **End-to-end gate**: `make e2e-meta-autotools-native`
  drives the full pipeline against
  `testdata/meta-project/autotools-greet/sources/` through
  bazel build. Asserts both `install_tree.tar` and a native
  `BUILD.bazel.out` are produced; the BUILD.bazel.out
  declares `cc_binary(name="greet", srcs=["greet.c"])`.

## Future directions

### make -p / make-database hint as a second input

Today's converter recovers structure purely from execve
sequences (compile output `.o` paired with later archive +
link). For `kind:make` and `kind:autotools`, `make -np`
(dry-run + print-database) would dump the Makefile's rule
graph directly: targets, prerequisites, recipes, variables.
A future pass could:

1. After the actual build completes inside the genrule, run
   `make -np` to capture the database alongside the trace.
2. Pass both artifacts to the converter
   (`--make-db=<path>`).
3. Use the database for higher-fidelity rule recovery:
   - Target names from Makefile (`myapp:`) → Bazel rule
     names, instead of inferring from the link command's
     `-o` argument.
   - Phony targets (`install`, `check`) for surfacing
     install-tree split points.
   - Per-target variable values (CFLAGS for myapp vs CFLAGS
     for libfoo) when Makefiles override per-target.
   - Cross-validate the trace's correlation against the
     Makefile's declared dep edges.

The execve trace alone is sufficient for the spike. Adding
make-database hints is additive — same architecture, richer
introspection. Worth picking up when `kind:autotools` corpus
expansion surfaces the cases the current correlation misses.

### Native ptrace beyond linux/amd64

The native ptrace backend is amd64-only today (register
layout, syscall number, calling convention are
arch-specific). Adding aarch64 / armv7 / ppc64le requires
roughly 50 lines of arch-specific code per arch
(`native_linux_<arch>.go` with the right register
struct field accesses + syscall numbers). Other GOOS/GOARCH
combos fall back to the strace shim transparently.

### RBE service-managed tracing

If buildbarn / buildgrid / EngFlow / similar RBE services
expose process tracing as a server-side feature, we'd drop
the in-action `build-tracer` wrapper entirely and inherit
the service's deterministic-trace semantics. Wishlist item
parked for the RBE community — trace export from the
executor would benefit anyone introspecting opaque tools,
not just our converter.
