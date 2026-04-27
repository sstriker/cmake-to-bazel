// orchestrate walks a BuildStream-style element tree, runs convert-element
// on every kind:cmake element in dependency-first order, and stages the
// outputs under <out>/.
//
// M3a: per-element execution is os/exec against a real convert-element
// binary. M3b will swap the same call shape for a REAPI Action submission.
//
// Run with --help to see flags.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/element"
	"github.com/sstriker/cmake-to-bazel/orchestrator/internal/orchestrator"
)

func main() {
	fs := flag.NewFlagSet("orchestrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		fdsdkRoot       = fs.String("fdsdk-root", "", "path to BuildStream root containing elements/")
		elementsDir     = fs.String("elements-dir", "elements", "subdirectory under --fdsdk-root holding .bst files")
		out             = fs.String("out", "out", "output root for converted elements + manifest")
		sourcesBase     = fs.String("sources-base", "", "directory containing pre-staged source trees per element name (overrides per-element kind:local sources)")
		converterBinary = fs.String("converter", "convert-element", "convert-element binary path or PATH name")
	)

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(64)
	}
	if *fdsdkRoot == "" {
		fmt.Fprintln(os.Stderr, "orchestrate: --fdsdk-root is required")
		fs.Usage()
		os.Exit(64)
	}

	proj, err := element.ReadProject(*fdsdkRoot, *elementsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orchestrate: %v\n", err)
		os.Exit(1)
	}
	g, err := element.BuildGraph(proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orchestrate: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	res, err := orchestrator.Run(ctx, orchestrator.Options{
		Project:         proj,
		Graph:           g,
		Out:             *out,
		SourcesBase:     *sourcesBase,
		ConverterBinary: *converterBinary,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "orchestrate: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "converted %d, failed %d\n", len(res.Converted), len(res.Failed))
	if len(res.Failed) > 0 {
		os.Exit(2)
	}
}
