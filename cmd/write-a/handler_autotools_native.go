package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sstriker/cmake-to-bazel/internal/tracecache"
)

// autotoolsConfig holds the render-time settings for the
// trace-driven autotools converter. Populated from main()'s
// flags before the per-element render loop runs. Empty
// cacheRoot disables the native render path entirely
// (kind:autotools always falls back to the coarse pipeline).
//
// Package-level state keeps the kindHandler interface small
// (RenderA / RenderB don't need an extra config arg) while
// letting the autotools handler branch on cache state per-
// element.
var autotoolsConfig struct {
	cacheRoot     string // --trace-cache-root
	tracerVersion string // --tracer-version
	convertBin    string // absolute path to convert-element-autotools
}

func init() {
	// kind:autotools registration. The existing pipelineHandler
	// covers the coarse install_tree.tar shape; we wrap it in
	// nativeAutotools so a populated trace cache flips the
	// per-element render to the trace-driven native path.
	registerHandler(nativeAutotoolsHandler{
		pipeline: autotoolsPipelineHandler(),
	})
}

// nativeAutotoolsHandler dispatches between the trace-driven
// native render (when the trace cache has an entry for the
// element's srckey) and the coarse pipeline render
// (otherwise). RenderB and the other kindHandler methods
// delegate unchanged — only RenderA branches.
type nativeAutotoolsHandler struct {
	pipeline pipelineHandler
}

func (h nativeAutotoolsHandler) Kind() string           { return "autotools" }
func (h nativeAutotoolsHandler) NeedsSources() bool     { return h.pipeline.NeedsSources() }
func (h nativeAutotoolsHandler) HasProjectABuild() bool { return h.pipeline.HasProjectABuild() }
func (h nativeAutotoolsHandler) DefaultReadPathsPatterns() *readPathsPatterns {
	return h.pipeline.DefaultReadPathsPatterns()
}

func (h nativeAutotoolsHandler) RenderA(elem *element, elemPkg string) error {
	if !nativeAutotoolsEnabled() {
		return h.pipeline.RenderA(elem, elemPkg)
	}
	hit, key, err := autotoolsCacheHit(elem)
	if err != nil {
		return fmt.Errorf("autotools native render: %w", err)
	}
	if !hit {
		return h.pipeline.RenderA(elem, elemPkg)
	}
	return renderNativeAutotools(elem, elemPkg, key)
}

func (h nativeAutotoolsHandler) RenderB(elem *element, elemPkg string) error {
	return h.pipeline.RenderB(elem, elemPkg)
}

// nativeAutotoolsEnabled reports whether main() supplied the
// flags needed to run the native render path (cache root +
// converter binary). Without either, every kind:autotools
// element falls back to the pipeline handler.
func nativeAutotoolsEnabled() bool {
	return autotoolsConfig.cacheRoot != "" && autotoolsConfig.convertBin != ""
}

// autotoolsCacheHit checks the trace cache for an entry
// matching this element's srckey + the configured tracer
// version. Returns the constructed Key alongside the hit/miss
// boolean so RenderNative can re-use it for the lookup.
func autotoolsCacheHit(elem *element) (bool, tracecache.Key, error) {
	srckey := elementSrcKey(elem)
	if srckey == "" {
		// kind:local sources without a content-key (the v1
		// fixture shape) get a deterministic placeholder
		// derived from the element name. Production replaces
		// this with the @src_<key>// digest.
		srckey = elem.Name
	}
	key := tracecache.Key{
		SrcKey:        srckey,
		TracerVersion: autotoolsConfig.tracerVersion,
	}
	has, err := tracecache.Has(autotoolsConfig.cacheRoot, key)
	return has, key, err
}

// elementSrcKey returns the element's first source's content-
// addressed srckey, or "" when no key is available (kind:local
// sources without a hash). Wraps the existing sourceKey()
// helper so the native autotools path doesn't need to peer at
// resolvedSource internals.
func elementSrcKey(elem *element) string {
	if len(elem.Sources) == 0 {
		return ""
	}
	return sourceKey(elem.Sources[0])
}

// renderNativeAutotools emits the cache-hit-path BUILD.bazel:
// stages the cached trace into the element package, emits a
// genrule that runs convert-element-autotools against it.
// Outputs mirror kind:cmake's: BUILD.bazel.out (the converted
// rules) plus an empty cmake-config-bundle.tar placeholder so
// downstream consumers can keep using the existing
// cross-element label shape.
func renderNativeAutotools(elem *element, elemPkg string, key tracecache.Key) error {
	if err := os.MkdirAll(elemPkg, 0o755); err != nil {
		return err
	}
	tracePath := filepath.Join(elemPkg, "trace.log")
	if err := tracecache.Lookup(autotoolsConfig.cacheRoot, key, tracePath); err != nil {
		return fmt.Errorf("lookup cached trace: %w", err)
	}
	build := autotoolsNativeBuildBody(elem.Name)
	return writeFile(filepath.Join(elemPkg, "BUILD.bazel"), build)
}

// autotoolsNativeBuildBody renders the per-element BUILD.bazel
// for the cache-hit path. The genrule is intentionally minimal:
// it consumes the staged trace.log + an optional imports.json,
// runs convert-element-autotools, and produces BUILD.bazel.out.
//
// imports.json is referenced via the same convention as
// kind:cmake's cross-element handle (an in-tree filegroup
// downstream consumers can label-reference). For now the
// element renders without an imports.json by default;
// downstream needs cross-element resolution will surface in
// a follow-up.
func autotoolsNativeBuildBody(name string) string {
	return fmt.Sprintf(`# Generated by cmd/write-a (kind:autotools native render). Do not edit by hand.

package(default_visibility = ["//visibility:public"])

# trace.log was staged from the trace cache at write-a time.
# Source-srckey + tracer-version match across nodes, so all
# project A renders for this element pull the same trace bytes.
filegroup(
    name = "%[1]s_trace",
    srcs = ["trace.log"],
)

genrule(
    name = "%[1]s_converted",
    srcs = [":%[1]s_trace"],
    outs = ["BUILD.bazel.out"],
    cmd = """
        $(location //tools:convert-element-autotools) \\
            --trace="$(location :%[1]s_trace)" \\
            --out-build="$(location BUILD.bazel.out)"
    """,
    tools = ["//tools:convert-element-autotools"],
)

filegroup(
    name = "build_bazel",
    srcs = ["BUILD.bazel.out"],
)
`, name)
}
