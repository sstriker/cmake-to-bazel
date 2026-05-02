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

## Future directions (parked)

In rough priority order:

1. **Per-target CFLAGS cross-validation**. Parse Makefile
   target-specific variable assignments (`foo: CC = clang`)
   from the make-db. Detect when the trace-recorded copts
   diverge from what the Makefile declared (e.g.,
   environmental override broke a per-target flag). Surface
   diffs as comments in BUILD.bazel.out — audit-grade.
   Needs a fixture where the Makefile uses target-specific
   vars; the autotools-multitarget fixture's helper.o uses
   recipe-level `-Wall` instead, so this gap surfaces only
   when a real-world fixture exercises it.
2. **Makefile target-name authority**. When the Makefile
   target name differs from the trace's `-o` argument
   basename, prefer the Makefile's. Mainly for shared libs
   with versioned filenames (`libfoo.so.0.1.0`) where
   Bazel's natural rule name differs from the on-disk
   output. No current fixture exercises this — defer until
   a real-world shared-versioned target surfaces the need.
3. **Phony-target recipe parsing beyond `install:`**.
   `check:` recipes describe test invocations (Bazel
   cc_test); `clean:` is structural noise; custom phony
   targets (`docs:`, `dist:`) might surface other typed
   slices. The current parser only walks `install:`.

## Standard autotools project as test bed

The hand-rolled `testdata/meta-project/autotools-multitarget/`
fixture exercises the full surface today (multiple cc_library
+ cc_binary outputs, multiple install dests, per-target
CFLAGS). A separate "real-world standard autotools project"
test bed (libpng-static / GNU hello / a coreutils slice) is a
nice-to-have for confidence at scale; deferred until the
spike itself surfaces a gap the multitarget fixture can't
cover.
