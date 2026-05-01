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
			"python":        "python3",
			"pip":           "pip",
			"python-prefix": "%{prefix}/lib/python3",
			"pip-args":      `--no-build-isolation --no-deps --no-index --target="%{install-root}%{python-prefix}"`,
			// build / installer driver knobs — names mirror
			// upstream buildstream-plugins pyproject defaults so
			// elements that reference them resolve cleanly.
			"build-args":     "--wheel --no-isolation",
			"installer-args": "",
			"dist-dir":       "dist",
		},
		defaults: pipelineDefaults{
			// Match upstream buildstream-plugins pyproject.yaml's
			// shape: %{python} -m build, then %{python} -m pip
			// install. Per-element variables: blocks override
			// pieces (e.g. setting python=python3.11 explicitly,
			// or extending pip-args with --extra-index-url).
			Build: []string{
				`%{python} -m build --wheel --no-isolation --outdir _bst_dist .`,
			},
			Install: []string{
				`%{python} -m %{pip} install %{pip-args} _bst_dist/*.whl`,
			},
		},
	})
}
