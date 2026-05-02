package main

func init() {
	// kind:makemaker is BuildStream's `makemaker` plugin lowered
	// onto the pipelineHandler shape. The plugin builds Perl
	// modules that ship a Makefile.PL via Perl's MakeMaker:
	//
	//	configure-commands: ["perl Makefile.PL PREFIX=%{prefix} INSTALLDIRS=vendor"]
	//	build-commands:     ["make"]
	//	install-commands:   ["make DESTDIR=\"%{install-root}\" install"]
	//
	// Defaults mirror BuildStream's plugin
	// (buildstream-plugins/src/buildstream_plugins/elements/makemaker.py).
	// FDSDK uses kind:makemaker for Perl modules in the bootstrap
	// path (perl-build, perl modules under bootstrap/base-sdk/).
	// Surfaced empirically by aom.bst's subgraph at
	// components/perl-build.
	registerHandler(pipelineHandler{
		kindName: "makemaker",
		defaults: pipelineDefaults{
			Configure: []string{
				`perl Makefile.PL PREFIX=%{prefix} INSTALLDIRS=vendor`,
			},
			Build: []string{
				`make`,
			},
			Install: []string{
				`make DESTDIR="%{install-root}" install`,
			},
		},
	})
}
