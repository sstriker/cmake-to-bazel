package main

func init() {
	// kind:make is BuildStream's `make` plugin lowered onto the
	// pipelineHandler shape. The plugin's defaults (per
	// buildstream/src/buildstream/plugins/elements/make.py) are:
	//
	//	build-commands:   ["make %{make-args}"]
	//	install-commands: ["make -j1 %{make-install-args}"]
	//
	// with the variable defaults:
	//
	//	make-args         = ""
	//	make-install-args = `DESTDIR="%{install-root}" install`
	//
	// The variable resolver (variables.go) expands these at codegen
	// time: %{make-install-args} is fully replaced with the
	// substituted RHS, then the runtime sentinel %{install-root}
	// becomes $$INSTALL_ROOT during command rendering. End result is
	// the same `make -j1 DESTDIR="$$INSTALL_ROOT" install` an
	// FDSDK-style .bst would emit, and an element overriding
	// %{make-install-args} (or %{make-args}) gets per-element
	// behavior without re-stating the surrounding `make ...` shape.
	registerHandler(pipelineHandler{
		kindName: "make",
		defaultVars: map[string]string{
			"make-args":         "",
			"make-install-args": `DESTDIR="%{install-root}" install`,
		},
		defaults: pipelineDefaults{
			Build: []string{
				`make %{make-args}`,
			},
			Install: []string{
				`make -j1 %{make-install-args}`,
			},
		},
	})
}
