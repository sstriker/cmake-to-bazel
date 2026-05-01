// source-push uploads on-disk source trees to a REAPI CAS
// endpoint, indexed by sourceKey, so cmd/cas-fuse can serve them
// to Bazel repo rules.
//
// Two modes:
//
//	source-push tree --cas=<grpc-addr> --src=<dir> [--instance=<name>]
//	  Pack a single directory tree, push every blob into CAS,
//	  print the root Directory digest. Useful for hello-world
//	  fixtures and dev workflows.
//
//	source-push graph --cas=<grpc-addr> --bst=<root.bst> --source-cache=<dir> [--instance=<name>]
//	  Walk a .bst graph, find each non-kind:local source's
//	  source-cache entry, pack + push each, and print a JSON
//	  manifest of {key → digest}. Used by make fdsdk-source-push.
//
// In production, BuildStream's `bst source push` is the canonical
// path — it knows how to fetch sources too. cmd/source-push
// covers the test/dev case where a populated --source-cache
// already exists and BuildStream isn't installed locally.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/sstriker/cmake-to-bazel/internal/casfuse"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "tree":
		cmdTree(os.Args[2:])
	case "graph":
		cmdGraph(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `source-push — upload local source trees to REAPI CAS.

Usage:
  source-push tree  --cas=<addr> --src=<dir> [--instance=<name>]
  source-push graph --cas=<addr> --source-cache=<dir> [--instance=<name>]
  source-push help
`)
}

func cmdTree(args []string) {
	fs := flag.NewFlagSet("tree", flag.ExitOnError)
	cas := fs.String("cas", "", "gRPC address of the CAS endpoint")
	src := fs.String("src", "", "directory to pack and push")
	instance := fs.String("instance", "", "REAPI instance name")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *cas == "" || *src == "" {
		fmt.Fprintln(os.Stderr, "--cas and --src are required")
		os.Exit(2)
	}

	pt, err := casfuse.PackDir(*src)
	if err != nil {
		log.Fatalf("pack %s: %v", *src, err)
	}

	client := dial(*cas, *instance)
	if err := pushAll(context.Background(), client, pt.Blobs); err != nil {
		log.Fatalf("push: %v", err)
	}
	fmt.Println(pt.RootDigest.String())
}

func cmdGraph(args []string) {
	fs := flag.NewFlagSet("graph", flag.ExitOnError)
	cas := fs.String("cas", "", "gRPC address of the CAS endpoint")
	cache := fs.String("source-cache", "", "directory of pre-fetched source trees, indexed by source-key")
	instance := fs.String("instance", "", "REAPI instance name")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *cas == "" || *cache == "" {
		fmt.Fprintln(os.Stderr, "--cas and --source-cache are required")
		os.Exit(2)
	}

	entries, err := os.ReadDir(*cache)
	if err != nil {
		log.Fatalf("read source-cache %s: %v", *cache, err)
	}
	client := dial(*cas, *instance)
	manifest := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		key := e.Name()
		path := *cache + "/" + key
		pt, err := casfuse.PackDir(path)
		if err != nil {
			log.Fatalf("pack %s: %v", path, err)
		}
		if err := pushAll(context.Background(), client, pt.Blobs); err != nil {
			log.Fatalf("push %s: %v", key, err)
		}
		manifest[key] = pt.RootDigest.String()
	}
	out, _ := json.MarshalIndent(manifest, "", "  ")
	fmt.Println(string(out))
}

func dial(addr, instance string) *casfuse.CASClient {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("dial CAS %q: %v", addr, err)
	}
	return casfuse.NewCASClient(conn, instance)
}

func pushAll(ctx context.Context, client *casfuse.CASClient, blobs map[string][]byte) error {
	for hash, body := range blobs {
		d := casfuse.Digest{Hash: hash, Size: int64(len(body))}
		if err := client.PushBlob(ctx, d, body); err != nil {
			return fmt.Errorf("blob %s: %w", hash, err)
		}
	}
	return nil
}
