// orchestrate walks a BuildStream-style element tree, runs convert-element
// on every kind:cmake element in dependency-first order, and stages the
// outputs under <out>/.
//
// M5: per-element conversions are wrapped in a real REAPI Action +
// ActionCache flow. --cas selects the cache substrate:
//
//	--cas=local:<path>    in-process filesystem CAS+AC (default; offline).
//	--cas=grpc://host:port  REAPI gRPC endpoint; --cas-* flags configure
//	                      TLS / token credentials.
//
// Independent orchestrator instances pointed at the same gRPC CAS share
// cache hits via standard ActionCache lookups. M3b will plug remote
// execution into the same Action shapes; M5 itself still runs the
// converter locally.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sstriker/cmake-to-bazel/internal/cas"
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
		casFlag         = fs.String("cas", "", "cache substrate: local:<path> | grpc://host:port | grpcs://host:port (default: local:<out>/cache)")
		casInstance     = fs.String("cas-instance", "", "REAPI instance_name (gRPC mode only)")
		casCert         = fs.String("cas-tls-cert", "", "client certificate file for mTLS (gRPC mode only)")
		casKey          = fs.String("cas-tls-key", "", "client private key file for mTLS (gRPC mode only)")
		casCA           = fs.String("cas-ca", "", "trust-root CA bundle (gRPC mode only)")
		casToken        = fs.String("cas-token-file", "", "file containing a bearer token (gRPC mode only)")
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
	store, closer, err := openStore(ctx, *casFlag, *out, casOpts{
		Instance:    *casInstance,
		TLSCertFile: *casCert,
		TLSKeyFile:  *casKey,
		CAFile:      *casCA,
		TokenFile:   *casToken,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "orchestrate: cas: %v\n", err)
		os.Exit(1)
	}
	if closer != nil {
		defer closer()
	}

	res, err := orchestrator.Run(ctx, orchestrator.Options{
		Project:         proj,
		Graph:           g,
		Out:             *out,
		SourcesBase:     *sourcesBase,
		ConverterBinary: *converterBinary,
		Store:           store,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "orchestrate: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "converted %d, failed %d (cache hits %d, misses %d)\n",
		len(res.Converted), len(res.Failed), len(res.CacheHits), len(res.CacheMisses))
	if len(res.Failed) > 0 {
		os.Exit(2)
	}
}

type casOpts struct {
	Instance    string
	TLSCertFile string
	TLSKeyFile  string
	CAFile      string
	TokenFile   string
}

// openStore parses the --cas flag and returns a Store ready for the
// orchestrator. Returns a non-nil closer for gRPC stores; LocalStore
// has no resources to release.
func openStore(ctx context.Context, casFlag, out string, opts casOpts) (cas.Store, func(), error) {
	if casFlag == "" {
		casFlag = "local:" + out + "/cache"
	}
	switch {
	case strings.HasPrefix(casFlag, "local:"):
		path := strings.TrimPrefix(casFlag, "local:")
		s, err := cas.NewLocalStore(path)
		if err != nil {
			return nil, nil, err
		}
		return s, nil, nil
	case strings.HasPrefix(casFlag, "grpc://"), strings.HasPrefix(casFlag, "grpcs://"):
		s, err := cas.NewGRPCStore(ctx, cas.GRPCConfig{
			Endpoint:     casFlag,
			InstanceName: opts.Instance,
			TLSCertFile:  opts.TLSCertFile,
			TLSKeyFile:   opts.TLSKeyFile,
			CAFile:       opts.CAFile,
			TokenFile:    opts.TokenFile,
		})
		if err != nil {
			return nil, nil, err
		}
		return s, func() { _ = s.Close() }, nil
	default:
		return nil, nil, fmt.Errorf("unknown --cas scheme %q (want local:<path> | grpc://... | grpcs://...)", casFlag)
	}
}
