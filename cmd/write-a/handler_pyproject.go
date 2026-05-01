package main

func init() {
	// kind:pyproject is BuildStream's `pyproject` plugin lowered
	// onto the pipelineHandler shape. The plugin builds Python
	// projects that ship a pyproject.toml — typically via
	// `python -m build` (or an equivalent PEP 517 frontend) and
	// `pip install` of the produced wheel into %{install-root}.
	//
	// Defaults mirror BuildStream's plugin
	// (buildstream-plugins/src/buildstream_plugins/elements/pyproject.py):
	//
	//	configure-commands: ["pip install --no-build-isolation --no-deps --no-index --target=%{install-root}%{python-prefix} ."]
	//	(no build / install / strip phases — pip install handles
	//	 build + stage in one step)
	//
	// with the variable default:
	//
	//	python-prefix = "%{prefix}/lib/python3"
	//
	// FDSDK uses kind:pyproject for python-only build deps
	// (flit-core, hatchling, setuptools, …). Surfaced empirically
	// by aom.bst's subgraph at components/_private/python3-flit-core.
	registerHandler(pipelineHandler{
		kindName: "pyproject",
		defaultVars: map[string]string{
			"python-prefix": "%{prefix}/lib/python3",
		},
		defaults: pipelineDefaults{
			Configure: []string{
				`pip install --no-build-isolation --no-deps --no-index --target="%{install-root}%{python-prefix}" .`,
			},
		},
	})
}
