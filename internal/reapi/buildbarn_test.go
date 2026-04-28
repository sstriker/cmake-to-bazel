//go:build buildbarn

// buildbarn_test exercises the M3b REAPI Execute path against a real
// Buildbarn deployment brought up via deploy/buildbarn/docker-compose.yml.
//
// Distinct from the M5 cache-share test in
// orchestrator/internal/orchestrator/buildbarn_test.go, which only
// hits CAS+AC. This one submits an Action through the scheduler at
// :8983 and verifies the worker actually runs it.
//
// We don't drive the converter end-to-end here — the bb-runner-bare
// image has /bin/sh but no cmake/ninja/bwrap, so we synthesize a
// trivial Action whose Command is a /bin/sh -c that writes a sentinel
// file. That's enough to validate the protocol round trip:
//
//   - input root + Command + Action proto land in CAS
//   - scheduler accepts the Execute submission
//   - worker pulls, materializes, exec's, packages outputs
//   - ActionResult lands in AC, output blobs are reachable
//
// Gated behind the `buildbarn` build tag and skips at runtime if
// the scheduler endpoint isn't reachable.
package reapi_test

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/sstriker/cmake-to-bazel/internal/cas"
	"github.com/sstriker/cmake-to-bazel/internal/reapi"
)

func TestE2E_Buildbarn_ExecuteSyntheticAction(t *testing.T) {
	casEndpoint := envOr("BUILDBARN_CAS_ENDPOINT", "127.0.0.1:8980")
	execEndpoint := envOr("BUILDBARN_EXEC_ENDPOINT", "127.0.0.1:8983")

	if err := probe(casEndpoint); err != nil {
		t.Skipf("Buildbarn CAS %s unreachable: %v\n  bring up with: docker compose -f deploy/buildbarn/docker-compose.yml up -d", casEndpoint, err)
	}
	if err := probe(execEndpoint); err != nil {
		t.Skipf("Buildbarn scheduler %s unreachable: %v", execEndpoint, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	store, err := cas.NewGRPCStore(ctx, cas.GRPCConfig{
		Endpoint: "grpc://" + casEndpoint,
		Insecure: true,
	})
	if err != nil {
		t.Fatalf("dial cas: %v", err)
	}
	defer store.Close()

	conn, err := grpc.NewClient(execEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial execute: %v", err)
	}
	defer conn.Close()
	executor := reapi.NewGRPCExecutor(conn, "")

	// Synthesize a minimal Action: empty input root, Command runs
	// /bin/sh -c "echo hello > BUILD.bazel", declares BUILD.bazel
	// as the only output. Platform properties match
	// deploy/buildbarn/config/worker.jsonnet so the scheduler
	// dispatches to our worker pool.
	emptyDir := &repb.Directory{}
	dirBlob, err := cas.MarshalDeterministic(emptyDir)
	if err != nil {
		t.Fatalf("marshal empty dir: %v", err)
	}
	emptyDigest := cas.DigestOf(dirBlob)
	if err := store.PutBlob(ctx, emptyDigest, dirBlob); err != nil {
		t.Fatalf("upload empty dir: %v", err)
	}

	cmd := &repb.Command{
		Arguments:   []string{"/bin/sh", "-c", "echo hello > BUILD.bazel"},
		OutputPaths: []string{"BUILD.bazel"},
		Platform: &repb.Platform{
			Properties: []*repb.Platform_Property{
				{Name: "Arch", Value: "x86_64"},
				{Name: "OSFamily", Value: "linux"},
				{Name: "bwrap-version", Value: "0.8.0"},
				{Name: "cmake-version", Value: "3.28.3"},
				{Name: "ninja-version", Value: "1.11.1"},
			},
		},
	}
	cmdDigest, cmdBlob, err := cas.DigestProto(cmd)
	if err != nil {
		t.Fatalf("digest cmd: %v", err)
	}
	if err := store.PutBlob(ctx, cmdDigest, cmdBlob); err != nil {
		t.Fatalf("upload cmd: %v", err)
	}

	action := &repb.Action{
		CommandDigest:   cmdDigest,
		InputRootDigest: emptyDigest,
		Platform:        cmd.Platform,
	}
	actionDigest, actionBlob, err := cas.DigestProto(action)
	if err != nil {
		t.Fatalf("digest action: %v", err)
	}
	if err := store.PutBlob(ctx, actionDigest, actionBlob); err != nil {
		t.Fatalf("upload action: %v", err)
	}

	built := &reapi.BuiltAction{
		Action:        action,
		ActionDigest:  actionDigest,
		ActionBlob:    actionBlob,
		Command:       cmd,
		CommandDigest: cmdDigest,
		CommandBlob:   cmdBlob,
		InputRoot: &reapi.InputRoot{
			Root:        emptyDir,
			RootDigest:  emptyDigest,
			Directories: map[string]*repb.Directory{emptyDigest.Hash: emptyDir},
			Files:       nil,
		},
		OutputPaths: []string{"BUILD.bazel"},
	}

	ar, err := executor.Execute(ctx, store, built)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if ar.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", ar.ExitCode)
	}
	if len(ar.OutputFiles) != 1 || ar.OutputFiles[0].Path != "BUILD.bazel" {
		t.Fatalf("OutputFiles = %+v, want [BUILD.bazel]", ar.OutputFiles)
	}

	// Worker actually wrote "hello\n"; fetch from CAS and verify.
	body, err := store.GetBlob(ctx, ar.OutputFiles[0].Digest)
	if err != nil {
		t.Fatalf("fetch output blob: %v", err)
	}
	if string(body) != "hello\n" {
		t.Errorf("output body = %q, want %q", body, "hello\n")
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func probe(addr string) error {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	return c.Close()
}
