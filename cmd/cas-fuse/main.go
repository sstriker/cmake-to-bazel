// cas-fuse mounts a REAPI-CAS Directory at a local path so Bazel
// repo rules can ctx.symlink into source trees that bst source
// push deposited into CAS, without ever materialising the bytes
// on the dev's local disk.
//
// v1 surface (this PR): single subcommand
//
//	cas-fuse mount --cas=<grpc-addr> --instance=<name> \
//	               --digest=<hash>-<size> --at=<mount-point>
//
// Blocks until SIGINT/SIGTERM, then unmounts cleanly.
//
// Subsequent PRs add:
//   - Multi-digest virtual root (one daemon serves many sources
//     via /<instance>/blobs/directory/<hash>-<size>/ paths,
//     mirroring bb_clientd's layout).
//   - macOS NFSv4 support.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

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
	case "mount":
		cmdMount(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `cas-fuse — FUSE-mount REAPI CAS Directory trees.

Usage:
  cas-fuse mount --cas=<grpc-addr> --digest=<hash>-<size> --at=<path> [--instance=<name>] [--allow-other]
  cas-fuse help
`)
}

func cmdMount(args []string) {
	fs := flag.NewFlagSet("mount", flag.ExitOnError)
	cas := fs.String("cas", "", "gRPC address of the CAS endpoint (host:port)")
	instance := fs.String("instance", "", "REAPI instance name (often empty)")
	digest := fs.String("digest", "", `root Directory digest in "<hash>-<size>" form`)
	at := fs.String("at", "", "mount point (must exist and be empty)")
	allowOther := fs.Bool("allow-other", false, "let other UIDs read the mount (needs user_allow_other in /etc/fuse.conf)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *cas == "" || *digest == "" || *at == "" {
		fmt.Fprintln(os.Stderr, "--cas, --digest, --at are required")
		fs.Usage()
		os.Exit(2)
	}

	d, err := casfuse.ParseDigest(*digest)
	if err != nil {
		log.Fatalf("parse --digest: %v", err)
	}

	conn, err := grpc.NewClient(*cas, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("dial CAS %q: %v", *cas, err)
	}
	defer conn.Close()

	client := casfuse.NewCASClient(conn, *instance)
	tree := casfuse.NewTree(client, d)

	server, err := casfuse.Mount(tree, *at, casfuse.MountOptions{AllowOther: *allowOther})
	if err != nil {
		log.Fatalf("mount %q: %v", *at, err)
	}
	log.Printf("cas-fuse mounted %s at %s (instance=%q)", d, *at, *instance)

	// Translate SIGINT/SIGTERM into a clean unmount.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Printf("cas-fuse unmounting %s", *at)
		_ = serverUnmount(server)
	}()
	serverWait(server)
}
