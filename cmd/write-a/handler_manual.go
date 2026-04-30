package main

func init() {
	// kind:manual is the most general pipeline kind: no defaults.
	// The .bst's config: block fully specifies which phase commands
	// run. Sibling kinds (kind:make / kind:autotools / ...) layer
	// default phase commands on the same pipelineHandler shape.
	registerHandler(pipelineHandler{kindName: "manual"})
}
