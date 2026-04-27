// Package ninja parses build.ninja into a graph the lowering stage uses to
// recover add_custom_command codegen as Bazel genrules.
//
// Scope is intentionally narrow: we only model what CMake-generated ninja
// files actually emit (rule, build, default, pool, include, subninja, top-
// level variable assignments). Out-of-scope: dynamic deps (deps = gcc/msvc
// dyndep files at build time), ninja's internal state, and anything that
// only matters for actually invoking ninja.
package ninja

// Graph is the parsed contents of a build.ninja (and any included files).
//
// Rule and Build bindings are stored verbatim as written; variable expansion
// is deferred to Expand so the same Build can be rendered against different
// scope overrides if a caller needs to.
type Graph struct {
	// Vars are top-level `name = value` assignments. Order matters in
	// ninja (a binding can reference earlier bindings); we preserve
	// insertion order via Var.
	Vars map[string]string

	// VarOrder is the names of Vars in declaration order. Useful for
	// deterministic output and for spotting forward-references.
	VarOrder []string

	// Rules is keyed by rule name. CMake usually emits a few dozen, one
	// per (compiler, target) pair plus shared CUSTOM_COMMAND/RERUN_CMAKE.
	Rules map[string]*Rule

	// Builds is the list of build statements in source order. Output
	// paths are unique, but a single Build can declare multiple outputs
	// — index them via OutputIndex if you need fast lookup.
	Builds []*Build

	// OutputIndex maps each output path (explicit and implicit) to the
	// Build that produces it. Built lazily by Index().
	OutputIndex map[string]*Build

	// Defaults is the set of default targets declared via `default ...`.
	// Order preserved.
	Defaults []string

	// Pools is keyed by pool name. CMake rarely uses these; we model them
	// for completeness.
	Pools map[string]*Pool
}

// Rule is one `rule <name>` block plus its indented bindings.
type Rule struct {
	Name     string
	Bindings map[string]string

	// BindingOrder preserves declaration order so command/description
	// resolution is deterministic.
	BindingOrder []string
}

// Build is one `build <outputs>: <rule> <inputs>...` statement plus its
// indented bindings.
//
// Per ninja syntax: `build out1 out2 | implicit_out : rule in1 in2 |
// implicit_in || order_only |@ validation`
type Build struct {
	Outputs        []string
	ImplicitOuts   []string
	Rule           string
	Inputs         []string
	ImplicitInputs []string
	OrderOnly      []string
	Validations    []string

	Bindings     map[string]string
	BindingOrder []string

	// Line is the 1-based line number of the `build` keyword in the
	// original file (or in an include after virtual concatenation). Used
	// in error messages and codegen-tag triage.
	Line int
}

// Pool is one `pool <name>` block.
type Pool struct {
	Name     string
	Bindings map[string]string
}

// Index populates g.OutputIndex from g.Builds. Idempotent.
func (g *Graph) Index() {
	g.OutputIndex = make(map[string]*Build, len(g.Builds))
	for _, b := range g.Builds {
		for _, o := range b.Outputs {
			g.OutputIndex[o] = b
		}
		for _, o := range b.ImplicitOuts {
			g.OutputIndex[o] = b
		}
	}
}

// BuildFor returns the Build that produces the given output path, or nil if
// no build statement claims it. Triggers Index() on first call.
func (g *Graph) BuildFor(out string) *Build {
	if g.OutputIndex == nil {
		g.Index()
	}
	return g.OutputIndex[out]
}

func newGraph() *Graph {
	return &Graph{
		Vars:  map[string]string{},
		Rules: map[string]*Rule{},
		Pools: map[string]*Pool{},
	}
}
