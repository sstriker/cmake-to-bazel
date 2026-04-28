# Codegen tag taxonomy

> Status: **stub for M2.** The taxonomy below is the contract M2 step 3
> emits against; this file is the user-facing reference. M2 fills in
> precise emission rules and adds the audit script. Once published, tag
> names are append-only — same stability rule as `failure-schema.md`.

Every Bazel rule produced by recovering an `add_custom_command` from a
`build.ninja` carries a stable `cmake-codegen` tag (and zero or more
sub-tags) so the entire converted project can be audited with `bazel
query` without scanning rule bodies.

## Producer-side tags

Applied to the `genrule` that the converter emits.

| Tag | When emitted | Stability |
|---|---|---|
| `cmake-codegen` | Always, on every recovered genrule. | append-only |
| `cmake-codegen-driver=<name>` | Always. `<name>` is the first command-token after `cd ... &&` (or the first token if no chdir), with wrappers (`env`, `sh -c`, `taskset`, …) stripped via a recognizer list. Falls back to `unknown` if extraction fails — never omitted. | append-only |
| `cmake-codegen-cmake-e` | Command invokes `${CMAKE_COMMAND} -E <op>` and the converter translated the op to a native Bazel idiom (e.g. `cp $< $@`). | append-only |
| `cmake-codegen-tool-from-target` | The driver tool is itself a target inside this element (typical of generator binaries built earlier in the same project). Useful for build-graph layering checks. | append-only |
| `cmake-codegen-source-only` | Output is consumed only as a `srcs`/`hdrs` entry of a downstream cc_library/cc_binary — i.e. the codegen exists purely to feed the compile graph. | append-only |
| `cmake-codegen-script` | Command runs `${CMAKE_COMMAND} -P script.cmake`. Architectural refusal: the converter emits this tag on the failing rule's placeholder so the operator sees the exact site, then exits with `failure.json` `code: unsupported-custom-command-script`. | append-only |

## Consumer-side tag

Applied to any `cc_library` / `cc_binary` whose `srcs` or `hdrs`
includes a path that comes from a `cmake-codegen`-tagged genrule.

| Tag | When emitted |
|---|---|
| `has-cmake-codegen` | The target depends on at least one codegen output transitively at the source-list level. |

## Why two-sided tagging

A single producer tag would force consumer-discovery queries to walk
the dep graph (slow at project scale, breaks across aliasing/renames).
A consumer-side `has-cmake-codegen` tag answers "which compile units
consume codegen?" in one query, independent of how the genrule is
labelled or aliased.

## Common queries

```sh
# Every recovered codegen rule in the project.
bazel query 'attr("tags", "cmake-codegen", //...)'

# Codegen rules driven by a specific tool.
bazel query 'attr("tags", "cmake-codegen-driver=python3", //...)'

# Targets that consume any codegen.
bazel query 'attr("tags", "has-cmake-codegen", //...)'

# Codegen rules that translate to native Bazel idioms (no cmake at runtime).
bazel query 'attr("tags", "cmake-codegen-cmake-e", //...)'
```

A wrapper at `tools/audit/list-codegen.sh` exposes these query shapes
plus a few cross-joins (codegen rules with their immediate consumers,
codegen rules grouped by driver tool).

## Stability promise

- Tag names listed above are stable and append-only after this document
  is published.
- New facets become new tags; existing tags don't change meaning.
- The audit script's flag surface is the same: removed flags get
  preserved as no-ops with a deprecation log line for one minor version.
