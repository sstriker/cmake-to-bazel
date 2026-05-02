package main

func init() {
	// kind:modulebuild is BuildStream's `modulebuild` plugin
	// lowered onto the pipelineHandler shape. Perl modules
	// shipping a Build.PL (Module::Build) instead of a Makefile.PL
	// (ExtUtils::MakeMaker) take this path:
	//
	//	configure-commands: ["perl Build.PL --prefix=%{prefix} --installdirs=vendor"]
	//	build-commands:     ["./Build"]
	//	install-commands:   ["./Build install --destdir \"%{install-root}\""]
	//
	// Defaults mirror BuildStream's plugin
	// (buildstream-plugins/src/buildstream_plugins/elements/modulebuild.py).
	// Surfaced empirically by aom.bst's subgraph at components/po4a.
	registerHandler(pipelineHandler{
		kindName: "modulebuild",
		defaults: pipelineDefaults{
			Configure: []string{
				`perl Build.PL --prefix=%{prefix} --installdirs=vendor`,
			},
			Build: []string{
				`./Build`,
			},
			Install: []string{
				`./Build install --destdir "%{install-root}"`,
			},
		},
	})
}
