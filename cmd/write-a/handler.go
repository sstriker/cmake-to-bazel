package main

// kindHandler is the per-kind contract for rendering one element's
// contribution to project A and project B. Each .bst kind write-a
// understands has exactly one handler in the `handlers` registry.
//
// Handlers are stateless — all per-element state lives on the element
// struct (graph metadata, source paths, narrowing feedback). The
// contract intentionally doesn't expose the graph object: parents
// reach their children via element.Deps.
//
// Implementations live in handler_<kind>.go (one file per kind) so
// adding a new kind is a self-contained change.
type kindHandler interface {
	// Kind returns the .bst `kind:` value this handler claims.
	// Used by the dispatch table and by error messages.
	Kind() string

	// NeedsSources reports whether elements of this kind have a
	// kind:local source tree write-a should resolve at parse time.
	// kind:cmake → true; kind:stack → false (it only references its
	// dependencies' outputs).
	NeedsSources() bool

	// HasProjectABuild reports whether this kind contributes a
	// per-element bazel-build action in project A. Driver scripts
	// loop over the graph and only invoke `bazel build` + the
	// staging step for elements where this returns true. Filegroup-
	// composition kinds (stack, filter, …) return false: their
	// project-A package is empty and project B's BUILD is the
	// writer's starlark output directly.
	HasProjectABuild() bool

	// RenderA writes the element's project-A package contents into
	// elemPkg (already created). For kinds with no A-side action,
	// this can be a no-op or a marker file.
	RenderA(elem *element, elemPkg string) error

	// RenderB writes the element's project-B package contents into
	// elemPkg (already cleared and re-created by writeProjectB).
	// Implementations are responsible for everything that goes in
	// the package — sources, BUILD.bazel, placeholders, etc.
	RenderB(elem *element, elemPkg string) error

	// DefaultReadPathsPatterns returns the converter-default
	// shadow-tree narrowing rules for this kind, or nil when the
	// kind doesn't narrow. Per-element <element>.read-paths.txt
	// rules layer on top (concatenated after the defaults), so
	// the default patterns shape the conservative starting point
	// and per-element files refine.
	//
	// Cache-key stability follows from the converter version:
	// when a kind's defaults change, the action input merkle for
	// every element of that kind shifts. Pinning the converter
	// version pins the defaults.
	//
	// Most kinds return nil — pipeline-shape kinds run arbitrary
	// commands that can read anything, so narrowing the source
	// tree at the genrule input layer is unsafe by default.
	// kind:cmake's narrowing is well-defined because cmake's
	// configure-time read pattern is structural.
	DefaultReadPathsPatterns() *readPathsPatterns
}

// handlers is the per-kind dispatch table. Each handler registers
// itself in init(); main() reads from this map to validate kinds and
// to dispatch RenderA / RenderB calls.
var handlers = map[string]kindHandler{}

func registerHandler(h kindHandler) {
	if _, ok := handlers[h.Kind()]; ok {
		panic("write-a: duplicate handler for kind:" + h.Kind())
	}
	handlers[h.Kind()] = h
}
