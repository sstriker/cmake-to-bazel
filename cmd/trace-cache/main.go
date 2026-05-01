// trace-cache is the CLI for the local-FS trace cache that
// stands in for REAPI Action Cache during the spike (see
// docs/trace-driven-autotools.md).
//
// Usage:
//
//	trace-cache register --root=<dir> --srckey=<key> --tracer-version=<v> --trace=<path>
//	trace-cache lookup   --root=<dir> --srckey=<key> --tracer-version=<v> --out=<path>
//	trace-cache has      --root=<dir> --srckey=<key> --tracer-version=<v>
//
// Production replaces this with REAPI Action Cache calls keyed
// by an action digest derived from (srckey, tracer_version).
// For the spike we need the simplest thing that lets project A
// look up a trace stored by project B's earlier round.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/sstriker/cmake-to-bazel/internal/tracecache"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "register":
		os.Exit(cmdRegister(args))
	case "lookup":
		os.Exit(cmdLookup(args))
	case "has":
		os.Exit(cmdHas(args))
	case "-h", "--help":
		usage()
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "trace-cache: unknown subcommand %q\n", cmd)
	usage()
	os.Exit(2)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: trace-cache <register|lookup|has> --root=<dir> --srckey=<k> --tracer-version=<v> [--trace=<path>|--out=<path>]")
}

func parseKey(fs *flag.FlagSet, args []string) (string, tracecache.Key, *flag.FlagSet) {
	root := fs.String("root", "", "cache root directory")
	srckey := fs.String("srckey", "", "element source-tree content-addressed key")
	tracer := fs.String("tracer-version", "", "build-tracer wire-format version")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *root == "" || *srckey == "" || *tracer == "" {
		fs.Usage()
		os.Exit(2)
	}
	return *root, tracecache.Key{SrcKey: *srckey, TracerVersion: *tracer}, fs
}

func cmdRegister(args []string) int {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	tracePath := fs.String("trace", "", "path to the trace artifact to store")
	root, key, fs := parseKey(fs, args)
	_ = fs
	if *tracePath == "" {
		fmt.Fprintln(os.Stderr, "trace-cache register: --trace required")
		return 2
	}
	if err := tracecache.Register(root, key, *tracePath); err != nil {
		fmt.Fprintf(os.Stderr, "trace-cache register: %v\n", err)
		return 1
	}
	return 0
}

func cmdLookup(args []string) int {
	fs := flag.NewFlagSet("lookup", flag.ExitOnError)
	out := fs.String("out", "", "path to write the cached trace")
	root, key, fs := parseKey(fs, args)
	_ = fs
	if *out == "" {
		fmt.Fprintln(os.Stderr, "trace-cache lookup: --out required")
		return 2
	}
	err := tracecache.Lookup(root, key, *out)
	if err == nil {
		return 0
	}
	if errors.Is(err, tracecache.ErrNotFound) {
		// Soft-miss exit code: 100. Distinguishes "no entry" from
		// "real error". Callers (write-a, scripts) can branch on
		// this without parsing stderr.
		return 100
	}
	fmt.Fprintf(os.Stderr, "trace-cache lookup: %v\n", err)
	return 1
}

func cmdHas(args []string) int {
	fs := flag.NewFlagSet("has", flag.ExitOnError)
	root, key, fs := parseKey(fs, args)
	_ = fs
	has, err := tracecache.Has(root, key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "trace-cache has: %v\n", err)
		return 1
	}
	if !has {
		return 100
	}
	return 0
}
