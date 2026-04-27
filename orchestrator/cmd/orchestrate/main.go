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
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/sstriker/cmake-to-bazel/internal/cas"
	"github.com/sstriker/cmake-to-bazel/internal/reapi"
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
		remoteExec      = fs.String("execute", "", "remote execution endpoint: grpc://host:port | grpcs://host:port. when set, conversions submit a REAPI Action instead of forking convert-element locally")
		remoteExecInst  = fs.String("execute-instance", "", "REAPI Execute instance_name; defaults to --cas-instance")
		concurrency     = fs.Int("concurrency", 0, "max in-flight per-element conversions (<=0 = NumCPU). Topology is preserved; deps still land before dependents.")
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

	var executor reapi.Executor
	if *remoteExec != "" {
		instance := *remoteExecInst
		if instance == "" {
			instance = *casInstance
		}
		ex, exCloser, err := openExecutor(*remoteExec, instance, casOpts{
			TLSCertFile: *casCert,
			TLSKeyFile:  *casKey,
			CAFile:      *casCA,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "orchestrate: execute: %v\n", err)
			os.Exit(1)
		}
		defer exCloser()
		executor = ex
	}

	res, err := orchestrator.Run(ctx, orchestrator.Options{
		Project:         proj,
		Graph:           g,
		Out:             *out,
		SourcesBase:     *sourcesBase,
		ConverterBinary: *converterBinary,
		Store:           store,
		Executor:        executor,
		Concurrency:     *concurrency,
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

// openExecutor parses --execute and returns a REAPI Executor wired
// to its own gRPC connection. The returned closer must be called when
// orchestration is done. Returns an error on bad endpoint scheme or
// dial failure.
func openExecutor(endpoint, instance string, opts casOpts) (reapi.Executor, func(), error) {
	addr, scheme := splitEndpoint(endpoint)
	var dialOpts []grpc.DialOption
	switch scheme {
	case "grpc":
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	case "grpcs":
		tc, err := buildExecuteTLS(opts)
		if err != nil {
			return nil, nil, fmt.Errorf("execute tls: %w", err)
		}
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tc)))
	default:
		return nil, nil, fmt.Errorf("--execute: unknown scheme %q (want grpc:// or grpcs://)", endpoint)
	}
	conn, err := grpc.NewClient(addr, dialOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("execute dial %s: %w", addr, err)
	}
	return reapi.NewGRPCExecutor(conn, instance), func() { _ = conn.Close() }, nil
}

func splitEndpoint(raw string) (string, string) {
	switch {
	case strings.HasPrefix(raw, "grpc://"):
		return strings.TrimPrefix(raw, "grpc://"), "grpc"
	case strings.HasPrefix(raw, "grpcs://"):
		return strings.TrimPrefix(raw, "grpcs://"), "grpcs"
	default:
		return raw, ""
	}
}

// buildExecuteTLS mirrors cas.GRPCStore's TLS plumbing for the
// Executor's separate connection. Reading the same flags twice keeps
// the surface symmetric: same cert/key/ca apply to both transports.
func buildExecuteTLS(opts casOpts) (*tls.Config, error) {
	tc := &tls.Config{}
	if opts.CAFile != "" {
		pem, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca %s: no certs parsed", opts.CAFile)
		}
		tc.RootCAs = pool
	}
	if opts.TLSCertFile != "" {
		cert, err := tls.LoadX509KeyPair(opts.TLSCertFile, opts.TLSKeyFile)
		if err != nil {
			return nil, err
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	return tc, nil
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
