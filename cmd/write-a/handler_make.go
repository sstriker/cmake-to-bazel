package main

func init() {
	// kind:make is BuildStream's `make` plugin lowered onto the
	// pipelineHandler shape. The plugin's defaults (per
	// buildstream/src/buildstream/plugins/elements/make.py and
	// FDSDK's project.conf overlay) are roughly:
	//
	//	build-commands:   ["make %{make-args}"]
	//	install-commands: ["make -j1 %{make-install-args}"]
	//
	// where %{make-args} / %{make-install-args} expand to
	// `-j${num-cpus}` and `DESTDIR=%{install-root} install`
	// respectively in the default variables.
	//
	// Phase 3 supports only %{install-root} / %{prefix}, so
	// %{make-args} / %{make-install-args} get inlined here and the
	// fully-substituted defaults below match what BuildStream would
	// emit for an element with no per-element overrides. The
	// variable-parser PR will replace these inlined defaults with
	// the proper indirect expansion.
	registerHandler(pipelineHandler{
		kindName: "make",
		defaults: pipelineDefaults{
			Build: []string{
				`make`,
			},
			Install: []string{
				`make -j1 DESTDIR="%{install-root}" install`,
			},
		},
	})
}
