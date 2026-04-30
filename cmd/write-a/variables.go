package main

// BuildStream-shape variable resolver.
//
// Every pipeline-kind element's phase commands (configure / build /
// install / strip) may reference variables via %{name} syntax. The
// values come from three layers, lowest precedence first:
//
//  1. Project defaults (BuildStream's projectconfig.yaml baseline,
//     plus FDSDK's prefix=/usr overlay). Hardcoded in projectVars
//     below; project.conf parsing is a follow-up.
//  2. Per-kind defaults (the kind handler's own variable bindings,
//     e.g. kind:make defines %{make-args} / %{make-install-args}).
//  3. Per-element variables: block in the .bst.
//
// References resolve recursively until fixed-point; cycles among
// non-runtime variables are an error. Two variables — %{install-root}
// and %{build-root} — are runtime sentinels: their values are bound
// by the genrule cmd's exported shell variables rather than at
// codegen time, so they pass through pre-expansion as literal
// %{install-root} / %{build-root} and get substituted to
// $$INSTALL_ROOT / $$BUILD_ROOT during command rendering.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// varRefRE matches %{name} where name uses the BuildStream variable
// alphabet (letters / digits / dashes / underscores). We don't
// support nested references like %{outer-%{inner}}; BuildStream
// itself doesn't either.
var varRefRE = regexp.MustCompile(`%\{([a-zA-Z0-9_-]+)\}`)

// runtimeSentinels are variables whose values are set by the
// genrule cmd's exported environment rather than at codegen time.
// They're known to the resolver only so it doesn't error out when
// commands reference them; the resolver leaves their references
// intact, and substituteCmd swaps them for the shell-var form
// after the variable map's been applied.
var runtimeSentinels = map[string]string{
	"install-root": "$$INSTALL_ROOT",
	"build-root":   "$$BUILD_ROOT",
}

// projectVars is the BuildStream variable baseline every element
// inherits unless it overrides individually. Values mirror
// buildstream/data/projectconfig.yaml plus FDSDK's prefix=/usr
// overlay (BuildStream's stock prefix is /usr/local; FDSDK's
// project.conf overrides it). Project.conf parsing is a follow-up;
// hardcoding here lets pipeline kinds reference the canonical
// variable surface immediately.
var projectVars = map[string]string{
	"prefix":         "/usr",
	"exec_prefix":    "%{prefix}",
	"lib":            "lib",
	"bindir":         "%{prefix}/bin",
	"sbindir":        "%{prefix}/sbin",
	"libexecdir":     "%{prefix}/libexec",
	"libdir":         "%{prefix}/%{lib}",
	"includedir":     "%{prefix}/include",
	"datadir":        "%{prefix}/share",
	"mandir":         "%{prefix}/share/man",
	"infodir":        "%{prefix}/share/info",
	"localedir":      "%{prefix}/share/locale",
	"docdir":         "%{prefix}/share/doc",
	"sysconfdir":     "/etc",
	"localstatedir":  "/var",
	"sharedstatedir": "/var",
}

// resolveVars composes the layered variable map (project defaults
// then kind defaults then element overrides) and expands every
// reference until fixed-point. Returns name->resolved-value, with
// runtime sentinels preserved as %{install-root} / %{build-root}
// so substituteCmd can swap them for shell-var references at the
// command-rendering stage.
//
// Cycles among non-sentinel variables produce an error naming one
// participant. References to undefined variables produce an error
// naming the missing variable.
func resolveVars(kindVars, elemVars map[string]string) (map[string]string, error) {
	raw := map[string]string{}
	for k, v := range projectVars {
		raw[k] = v
	}
	for k, v := range kindVars {
		raw[k] = v
	}
	for k, v := range elemVars {
		raw[k] = v
	}

	resolved := map[string]string{}
	resolving := map[string]bool{}

	var resolve func(name string) (string, error)
	resolve = func(name string) (string, error) {
		if v, ok := resolved[name]; ok {
			return v, nil
		}
		if _, isSentinel := runtimeSentinels[name]; isSentinel {
			// Sentinel passes through expansion unless the user
			// explicitly overrode it (rare). The unoverridden case
			// short-circuits here so a command's %{install-root}
			// stays literal until substituteCmd's sentinel pass.
			if _, override := raw[name]; !override {
				resolved[name] = "%{" + name + "}"
				return resolved[name], nil
			}
		}
		template, ok := raw[name]
		if !ok {
			return "", fmt.Errorf("variable %q referenced but not defined", name)
		}
		if resolving[name] {
			return "", fmt.Errorf("variable cycle through %q", name)
		}
		resolving[name] = true
		defer delete(resolving, name)
		out, err := expandRefs(template, resolve)
		if err != nil {
			return "", err
		}
		resolved[name] = out
		return out, nil
	}

	// Resolve in deterministic order so an error message is stable
	// across runs (the regex iterates in name order anyway, but the
	// outer loop here matters when multiple cycles are present).
	// Sentinels are walked too so the returned map always carries
	// %{install-root} / %{build-root} entries regardless of whether
	// any layer happened to reference them.
	seen := map[string]struct{}{}
	names := make([]string, 0, len(raw)+len(runtimeSentinels))
	for k := range raw {
		names = append(names, k)
		seen[k] = struct{}{}
	}
	for k := range runtimeSentinels {
		if _, ok := seen[k]; !ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := resolve(name); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

// expandRefs replaces every %{name} reference in s with the result
// of lookup(name). The first lookup error short-circuits and
// surfaces from expandRefs; subsequent matches return the original
// %{...} literal so the partial output stays self-consistent.
func expandRefs(s string, lookup func(string) (string, error)) (string, error) {
	var firstErr error
	out := varRefRE.ReplaceAllStringFunc(s, func(match string) string {
		if firstErr != nil {
			return match
		}
		name := match[2 : len(match)-1]
		v, err := lookup(name)
		if err != nil {
			firstErr = err
			return match
		}
		return v
	})
	return out, firstErr
}

// substituteCmd applies the resolved variable map to a single phase
// command, then swaps runtime sentinels for shell-var references.
// References to unknown variables surface as errors so a typo in a
// .bst doesn't silently emit a literal %{misspelled} into the
// rendered genrule cmd.
func substituteCmd(cmd string, vars map[string]string) (string, error) {
	out, err := expandRefs(cmd, func(name string) (string, error) {
		v, ok := vars[name]
		if !ok {
			return "", fmt.Errorf("variable %q referenced but not defined", name)
		}
		return v, nil
	})
	if err != nil {
		return "", err
	}
	for sentinel, shellVar := range runtimeSentinels {
		out = strings.ReplaceAll(out, "%{"+sentinel+"}", shellVar)
	}
	return out, nil
}
