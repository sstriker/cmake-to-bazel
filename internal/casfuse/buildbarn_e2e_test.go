//go:build buildbarn

package casfuse

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestE2E_SourcePush: buildbarn-gated end-to-end. Pack a tiny
// synthetic tree, push every blob into the running buildbarn
// CAS via PushBlob, then read the same tree back through Tree
// to prove the wire format round-trips through real
// bb-storage. Invoked by `make e2e-source-push`, which stands
// up + tears down the docker-compose stack around it.
//
// CAS endpoint is the buildbarn-up Makefile target's published
// gRPC port. Override with CAS_ADDR if running standalone.
func TestE2E_SourcePush(t *testing.T) {
	addr := os.Getenv("CAS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8980"
	}

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "main.c"), []byte("int main(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pt, err := PackDir(src)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial buildbarn CAS at %s: %v", addr, err)
	}
	defer conn.Close()
	client := NewCASClient(conn, "")

	// Push.
	for hash, body := range pt.Blobs {
		d := Digest{Hash: hash, Size: int64(len(body))}
		if err := client.PushBlob(context.Background(), d, body); err != nil {
			t.Fatalf("PushBlob(%s) against real buildbarn: %v", hash, err)
		}
	}

	// Pull back via Tree, content-check.
	tree := NewTree(client, pt.RootDigest)
	got, err := tree.Lookup(context.Background(), "main.c")
	if err != nil {
		t.Fatalf("Lookup main.c after push: %v", err)
	}
	body, err := tree.ReadFile(context.Background(), got.FileNode)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "int main(){}\n" {
		t.Errorf("body mismatch after roundtrip: %q", body)
	}
}
