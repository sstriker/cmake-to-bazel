// Package orchestrator drives per-element conversions.
//
// The Run function walks the element graph in dependency-first topo order
// and invokes the converter once per kind:cmake element. M3a uses os/exec
// against a real `convert-element` binary; M3b will swap the same call
// shape for a REAPI Action submission.
//
// Outputs land under <Out>/elements/<elem-name>/. A successful conversion
// produces:
//
//	BUILD.bazel
//	cmake-config/<Pkg>{Config,Targets,Targets-release}.cmake
//	read_paths.json
//
// A Tier-1 failure produces failure.json under the same directory and the
// element is recorded in the global failures registry. Tier-2/3 failures
// (converter crashed, infrastructure error) propagate as Go errors that
// abort the whole orchestrator.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/element"
)

// Options configures one Run call.
type Options struct {
	// Project / Graph are the parsed FDSDK input. Run filters to
	// kind:cmake elements internally; the caller passes the full graph.
	Project *element.Project
	Graph   *element.Graph

	// Out is the output root; the canonical layout
	// (docs/m3-plan.md, "Output layout") is built underneath it.
	Out string

	// SourcesBase, when non-empty, takes precedence over per-element
	// `sources[].path` resolution. The orchestrator looks for each
	// element's source tree at <SourcesBase>/<element-name>/. Used by
	// the test fixture and by orchestrators that pre-stage sources.
	SourcesBase string

	// ConverterBinary is the path to the convert-element binary. Defaults
	// to "convert-element" (PATH lookup).
	ConverterBinary string

	// Log captures orchestrator progress messages and per-element
	// converter stdout/stderr (merged). Defaults to os.Stderr when nil.
	Log io.Writer
}

// Result summarizes a Run.
type Result struct {
	Converted []string
	Failed    []FailureRecord
}

// FailureRecord is the per-element entry the orchestrator collects for the
// global failures.json registry. Mirrors converter Tier-1 failure.json
// schema with an added Element field.
type FailureRecord struct {
	Element string `json:"element"`
	Tier    int    `json:"tier"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Run drives the conversion. Returns a populated Result on success even if
// some elements failed Tier-1; only Tier-2/3 (or orchestrator-level)
// errors return non-nil err.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.Project == nil || opts.Graph == nil {
		return nil, errors.New("orchestrator: Project and Graph required")
	}
	if opts.Out == "" {
		return nil, errors.New("orchestrator: Out required")
	}
	conv := opts.ConverterBinary
	if conv == "" {
		conv = "convert-element"
	}
	if _, err := exec.LookPath(conv); err != nil {
		return nil, fmt.Errorf("orchestrator: converter binary %q not on PATH: %w", conv, err)
	}

	order, err := opts.Graph.TopoSort()
	if err != nil {
		return nil, err
	}
	cmakeOrder := opts.Graph.FilterByKind(order, "cmake")

	if err := os.MkdirAll(filepath.Join(opts.Out, "elements"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(opts.Out, "manifest"), 0o755); err != nil {
		return nil, err
	}

	res := &Result{}
	for _, name := range cmakeOrder {
		el := opts.Project.Elements[name]
		srcRoot, err := resolveSource(el, opts.SourcesBase)
		if err != nil {
			return nil, fmt.Errorf("element %s: %w", name, err)
		}
		fmt.Fprintf(logOf(opts), "==> %s\n", name)

		fr, err := convertOne(ctx, conv, name, srcRoot, opts)
		if err != nil {
			return nil, err
		}
		if fr != nil {
			res.Failed = append(res.Failed, *fr)
			continue
		}
		res.Converted = append(res.Converted, name)
	}

	if err := writeManifest(opts.Out, res); err != nil {
		return nil, err
	}
	return res, nil
}

// convertOne runs the converter against one element. Returns (nil, nil) on
// success, (FailureRecord, nil) on Tier-1, (nil, err) on Tier-2/3.
func convertOne(ctx context.Context, conv, name, srcRoot string, opts Options) (*FailureRecord, error) {
	elemOut := filepath.Join(opts.Out, "elements", name)
	if err := os.MkdirAll(elemOut, 0o755); err != nil {
		return nil, err
	}

	args := []string{
		"--source-root", srcRoot,
		"--out-build", filepath.Join(elemOut, "BUILD.bazel"),
		"--out-bundle-dir", filepath.Join(elemOut, "cmake-config"),
		"--out-failure", filepath.Join(elemOut, "failure.json"),
		"--out-read-paths", filepath.Join(elemOut, "read_paths.json"),
	}

	cmd := exec.CommandContext(ctx, conv, args...)
	cmd.Stdout = logOf(opts)
	cmd.Stderr = logOf(opts)

	err := cmd.Run()
	if err == nil {
		return nil, nil
	}

	// Tier-1: convert-element exited 1 with a written failure.json.
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		fr, ferr := loadFailure(name, filepath.Join(elemOut, "failure.json"))
		if ferr != nil {
			return nil, fmt.Errorf("element %s: convert-element exit 1 but failure.json unreadable: %w", name, ferr)
		}
		return fr, nil
	}
	// Tier-2/3 or other unexpected exit. Bubble up.
	return nil, fmt.Errorf("element %s: convert-element: %w", name, err)
}

// resolveSource picks the source tree path for an element. If
// SourcesBase is set, uses <SourcesBase>/<element-name>. Otherwise falls
// back to the first `kind: local` source's path, resolved relative to the
// .bst file's directory.
func resolveSource(el *element.Element, sourcesBase string) (string, error) {
	if sourcesBase != "" {
		// Use the element name (with directory components) under the
		// shared base. e.g. components/hello -> <base>/components/hello.
		p := filepath.Join(sourcesBase, filepath.FromSlash(el.Name))
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("source dir %q: %w", p, err)
		}
		return p, nil
	}
	for _, s := range el.Sources {
		if s.Kind == "local" {
			path, ok := s.Extra["path"].(string)
			if !ok || path == "" {
				return "", errors.New("local source missing path")
			}
			abs := path
			if !filepath.IsAbs(path) {
				abs = filepath.Join(filepath.Dir(el.SourcePath), path)
			}
			abs = filepath.Clean(abs)
			if _, err := os.Stat(abs); err != nil {
				return "", fmt.Errorf("source dir %q: %w", abs, err)
			}
			return abs, nil
		}
	}
	return "", errors.New("no kind:local source; pass --sources-base to override")
}

func loadFailure(name, path string) (*FailureRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Tier    int    `json:"tier"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	return &FailureRecord{
		Element: name,
		Tier:    raw.Tier,
		Code:    raw.Code,
		Message: raw.Message,
	}, nil
}

// writeManifest writes converted.json + failures.json under <out>/manifest/.
//
// converted.json entries are sorted by element name for stable diffs;
// failures.json the same. Schema versioned via "version": 1 so M4's
// regression detector can fence on incompatible reads.
func writeManifest(out string, res *Result) error {
	conv := append([]string(nil), res.Converted...)
	sort.Strings(conv)
	type elemEntry struct {
		Name string `json:"name"`
	}
	convDoc := struct {
		Version  int         `json:"version"`
		Elements []elemEntry `json:"elements"`
	}{Version: 1}
	for _, n := range conv {
		convDoc.Elements = append(convDoc.Elements, elemEntry{Name: n})
	}
	if err := writeJSON(filepath.Join(out, "manifest", "converted.json"), convDoc); err != nil {
		return err
	}

	fails := append([]FailureRecord(nil), res.Failed...)
	sort.Slice(fails, func(i, j int) bool { return fails[i].Element < fails[j].Element })
	failDoc := struct {
		Version  int             `json:"version"`
		Elements []FailureRecord `json:"elements"`
	}{Version: 1, Elements: fails}
	return writeJSON(filepath.Join(out, "manifest", "failures.json"), failDoc)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func logOf(opts Options) io.Writer {
	if opts.Log != nil {
		return opts.Log
	}
	return os.Stderr
}
