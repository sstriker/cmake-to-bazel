package main

func init() {
	// kind:script is BuildStream's `script` plugin: a single flat
	// list of shell commands (config: commands: [...]) run in
	// order to produce an install tree. No configure / build /
	// strip phases — the simplest pipeline kind.
	//
	// 53 of FDSDK's elements use it (5 % of total). Common case:
	// staging or layout fixups that operate on dep install trees
	// rather than building from source — bootstrap directory-
	// stub elements, the pkgconfig-dir scripts FDSDK uses to
	// pre-create directories under %{install-root}, etc.
	//
	// Registers as a pipelineHandler with no per-phase defaults
	// (kind:script has no defaults to apply); the pipelineCfg's
	// Commands field maps onto the install-commands slot at
	// render time so the existing select() / variable resolver /
	// runtime-sentinel machinery flows through unchanged.
	registerHandler(pipelineHandler{kindName: "script"})
}
