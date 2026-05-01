package main

func init() {
	// kind:meson is BuildStream's `meson` plugin lowered onto the
	// pipelineHandler shape. Defaults mirror buildstream-plugins'
	// meson element (see meson.py in the plugin repo):
	//
	//	configure-commands: ["meson %{conf-cmd-args}"]
	//	build-commands:     ["meson compile -C %{meson-builddir} %{meson-build-args}"]
	//	install-commands:   ["env DESTDIR=\"%{install-root}\" meson install -C %{meson-builddir} %{meson-install-args}"]
	//
	// with the variable defaults:
	//
	//	meson-source       = "."
	//	meson-builddir     = "_builddir"
	//	conf-cmd-args      = "%{meson-source} %{meson-builddir}"
	//	meson-build-args   = ""
	//	meson-install-args = ""
	//
	// FDSDK uses kind:meson for ~50 elements (gobject-introspection,
	// glib-deps, mesa-deps, …). Surfaced empirically by aom.bst's
	// subgraph at components/_private/git-minimal.
	registerHandler(pipelineHandler{
		kindName: "meson",
		defaultVars: map[string]string{
			"meson-source":       ".",
			"meson-builddir":     "_builddir",
			"conf-cmd-args":      "%{meson-source} %{meson-builddir}",
			"meson-build-args":   "",
			"meson-install-args": "",
		},
		defaults: pipelineDefaults{
			Configure: []string{
				`meson %{conf-cmd-args}`,
			},
			Build: []string{
				`meson compile -C %{meson-builddir} %{meson-build-args}`,
			},
			Install: []string{
				`env DESTDIR="%{install-root}" meson install -C %{meson-builddir} %{meson-install-args}`,
			},
		},
	})
}
