package main

// BuildStream (@): YAML composition pre-processor.
//
// Real .bst and project.conf files declare cross-file composition
// via the `(@):` directive: a list of YAML files to load and deep-
// merge into the current map. 18 % of FDSDK elements use it at the
// element level; FDSDK's project.conf relies on it at the top level
// (`variables: (@): - include/_private/arch.yml ...`).
//
// composeYAML walks a parsed yaml.Node tree, resolves every (@):
// directive by loading the referenced file (recursively, with cycle
// detection) and merging its content into the parent map. The
// merge is parent-wins-on-conflict for both scalars and nested
// mappings (matches BuildStream's left-to-right composition where
// the local document's keys override the included content), and
// non-overlapping keys are added unchanged.
//
// Other BuildStream YAML directives surveyed in the FDSDK reality
// check land in separate work:
//
//   - (?): per-arch / per-target conditional variable overrides —
//     written away to project-B Starlark select() rather than
//     resolved at write-a time. v1 strips these keys after (@):
//     resolution so the unmarshal-into-struct step doesn't choke
//     on the unhandled shape.
//   - (>): / (<): / (=): — list append / prepend / overwrite. Not
//     yet observed in the curated probes; lands when forced.

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// loadAndComposeYAML reads a YAML file from path, parses it as a
// yaml.Node tree, resolves every (@): directive in the tree
// (recursively), strips other unhandled directive keys, and returns
// the resulting node ready for struct-decode by yaml.v3.
//
// includeBase is the directory (@): paths resolve against. Real
// BuildStream resolves them relative to the project root (the
// directory containing project.conf), NOT relative to the file
// containing the directive — so a runtime.yml at project/include/
// declaring `(@): - include/flags.yml` resolves the include to
// project/include/flags.yml (sibling), not project/include/include/.
//
// When no project.conf is found, callers pass filepath.Dir(path)
// as a fallback — the existing-fixture shape with self-contained
// .bst files.
//
// The visited set tracks absolute paths currently on the include
// stack so a cycle (A includes B includes A) surfaces as an error
// rather than recursing forever.
func loadAndComposeYAML(path, includeBase string, visited map[string]bool) (*yaml.Node, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if visited[abs] {
		return nil, fmt.Errorf("(@) include cycle through %s", abs)
	}
	visited[abs] = true
	defer delete(visited, abs)

	body, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", abs, err)
	}
	if err := composeYAML(&doc, includeBase, visited); err != nil {
		return nil, fmt.Errorf("compose %s: %w", abs, err)
	}
	resolveBareListMergeDirectives(&doc)
	stripUnhandledDirectives(&doc)
	return &doc, nil
}

// resolveBareListMergeDirectives walks node in place. When a
// mapping is `{(>): [...]}` / `{(<): [...]}` / `{(=): [...]}` —
// i.e. the map's only key is a BuildStream list-merge directive
// — the map collapses to the directive's list value. The
// directive's contract is "compose with the parent's list at this
// path"; with no parent context (the case write-a hits when
// loading a single file), the directive's value IS the resulting
// list.
//
// FDSDK shape that hit this: `sources: { (>): [...] }` in
// elements/components/linux.bst — the parent's list is implicitly
// empty, so the (>) value is just the source list.
func resolveBareListMergeDirectives(node *yaml.Node) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range node.Content {
			resolveBareListMergeDirectives(c)
		}
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			resolveBareListMergeDirectives(node.Content[i+1])
		}
		// Detect "this map has exactly one key, and it's a list-
		// merge directive." Collapse the map to the directive's
		// value (which should be a sequence).
		if len(node.Content) == 2 {
			k := node.Content[0].Value
			if k == "(>)" || k == "(<)" || k == "(=)" {
				v := node.Content[1]
				if v.Kind == yaml.SequenceNode {
					*node = *v
				}
			}
		}
	}
}

// composeYAML walks node in place, resolving (@): directives. For
// each mapping that contains a (@): key, the directive is removed
// from the mapping and its referenced files are loaded (relative to
// includeBase — the project root) and deep-merged into the
// mapping. Recursion descends into every value of every mapping
// and every entry of every sequence, so nested (@): directives are
// handled.
func composeYAML(node *yaml.Node, includeBase string, visited map[string]bool) error {
	if node == nil {
		return nil
	}
	switch node.Kind {
	case yaml.DocumentNode:
		for _, c := range node.Content {
			if err := composeYAML(c, includeBase, visited); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		// Find a (@): key — there's at most one per mapping per
		// BuildStream's schema.
		atIdx := -1
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == "(@)" {
				atIdx = i
				break
			}
		}
		if atIdx >= 0 {
			includesNode := node.Content[atIdx+1]
			// Excise the (@): key/value before merging includes —
			// otherwise mergeMappings would try to merge against the
			// directive itself.
			node.Content = append(node.Content[:atIdx], node.Content[atIdx+2:]...)
			files, err := includesAsFileList(includesNode)
			if err != nil {
				return err
			}
			for _, f := range files {
				incPath := filepath.Join(includeBase, f)
				included, err := loadAndComposeYAML(incPath, includeBase, visited)
				if err != nil {
					return err
				}
				// loadAndComposeYAML returns a DocumentNode wrapping
				// the actual content — unwrap to the inner map for
				// merging.
				inner := unwrapDocument(included)
				if inner == nil {
					return fmt.Errorf("(@) %s: file is empty", incPath)
				}
				if inner.Kind != yaml.MappingNode {
					return fmt.Errorf("(@) %s: included file must be a YAML mapping, got node kind %d", incPath, inner.Kind)
				}
				mergeMappings(node, inner)
			}
		}
		// Recurse into every value (and the keys for completeness,
		// though BuildStream's directives only appear as values).
		for i := 0; i < len(node.Content); i += 2 {
			if err := composeYAML(node.Content[i+1], includeBase, visited); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, c := range node.Content {
			if err := composeYAML(c, includeBase, visited); err != nil {
				return err
			}
		}
	}
	return nil
}

// includesAsFileList returns the list of include paths from a (@):
// directive's value. BuildStream accepts both a single string
// (single include) and a sequence (multiple includes).
func includesAsFileList(node *yaml.Node) ([]string, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		return []string{node.Value}, nil
	case yaml.SequenceNode:
		out := make([]string, 0, len(node.Content))
		for _, c := range node.Content {
			if c.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("(@) include list entry must be a string, got node kind %d", c.Kind)
			}
			out = append(out, c.Value)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("(@) value must be a string or list of strings, got node kind %d", node.Kind)
	}
}

// unwrapDocument returns the top-level content of a DocumentNode,
// or the node itself when it isn't a document. yaml.v3's Unmarshal
// always returns a DocumentNode wrapping the actual content.
func unwrapDocument(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		return node.Content[0]
	}
	return node
}

// mergeMappings deep-merges src into dst — both expected to be
// MappingNode kinds. Parent-wins on conflicts: dst's existing keys
// take precedence over src's; only when both have the same key
// pointing at a sub-mapping do we recurse to merge them. This
// matches BuildStream's "your local definitions override the
// included content" semantics. Non-overlapping keys from src are
// appended to dst.
func mergeMappings(dst, src *yaml.Node) {
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		return
	}
	// Index dst's existing keys.
	idx := map[string]int{}
	for i := 0; i < len(dst.Content); i += 2 {
		idx[dst.Content[i].Value] = i + 1 // value index
	}
	for i := 0; i < len(src.Content); i += 2 {
		k := src.Content[i].Value
		if vIdx, ok := idx[k]; ok {
			dstVal := dst.Content[vIdx]
			srcVal := src.Content[i+1]
			if dstVal.Kind == yaml.MappingNode && srcVal.Kind == yaml.MappingNode {
				mergeMappings(dstVal, srcVal)
			}
			// else: parent wins, nothing to do.
		} else {
			dst.Content = append(dst.Content, src.Content[i], src.Content[i+1])
		}
	}
}

// stripUnhandledDirectives removes BuildStream YAML directives
// write-a doesn't evaluate inline from the tree. For v1 that's
// the list-merge directives (>): / (<): / (=): — not yet observed
// in the curated probes; stripping them keeps decode robust if
// they show up in real FDSDK content.
//
// (@): is not in this list — composeYAML resolves it before this
// pass runs. (?): is not in this list either: variable-level (?):
// blocks are extracted into structured form by
// extractConditionalsFromVariables in conditional.go (so the
// pipeline handler can lower them to project-B select() over
// @platforms//cpu:*); callers must run that extraction before
// the struct-decode step or yaml.v3 will choke on the unhandled
// list-of-mapping shape.
func stripUnhandledDirectives(node *yaml.Node) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range node.Content {
			stripUnhandledDirectives(c)
		}
	case yaml.MappingNode:
		// Walk pairs, dropping any whose key is an unhandled
		// directive. Recurse into surviving values.
		out := node.Content[:0]
		for i := 0; i < len(node.Content); i += 2 {
			k := node.Content[i].Value
			if isUnhandledDirective(k) {
				continue
			}
			stripUnhandledDirectives(node.Content[i+1])
			out = append(out, node.Content[i], node.Content[i+1])
		}
		node.Content = out
	}
}

func isUnhandledDirective(k string) bool {
	switch k {
	case "(>)", "(<)", "(=)":
		return true
	default:
		return false
	}
}
