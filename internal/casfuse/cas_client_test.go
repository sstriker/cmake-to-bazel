package casfuse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"
	"testing"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

func TestParseDigest(t *testing.T) {
	cases := map[string]struct {
		want Digest
		err  bool
	}{
		"abc-42":              {Digest{Hash: "abc", Size: 42}, false},
		"abc":                 {Digest{}, true}, // missing separator
		"abc-notanumber":      {Digest{}, true},
		"deadbeef-1024":       {Digest{Hash: "deadbeef", Size: 1024}, false},
		"hash-with-dashes-99": {Digest{Hash: "hash-with-dashes", Size: 99}, false},
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := ParseDigest(in)
			if want.err {
				if err == nil {
					t.Errorf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != want.want {
				t.Errorf("got %+v, want %+v", got, want.want)
			}
		})
	}
}

func TestDigestStringRoundTrip(t *testing.T) {
	d := Digest{Hash: "abc", Size: 99}
	got, err := ParseDigest(d.String())
	if err != nil || got != d {
		t.Errorf("roundtrip: got %+v, err %v, want %+v", got, err, d)
	}
}

// fakeCAS is a tiny in-process REAPI CAS server backed by an
// in-memory map of digest → bytes. Just enough surface for the
// daemon's GetDirectory + ReadBlob paths to exercise.
type fakeCAS struct {
	repb.UnimplementedContentAddressableStorageServer
	bspb.UnimplementedByteStreamServer
	blobs map[string][]byte // hash → bytes
}

func (f *fakeCAS) BatchUpdateBlobs(_ context.Context, req *repb.BatchUpdateBlobsRequest) (*repb.BatchUpdateBlobsResponse, error) {
	out := &repb.BatchUpdateBlobsResponse{}
	for _, r := range req.Requests {
		f.blobs[r.Digest.Hash] = append([]byte(nil), r.Data...)
		out.Responses = append(out.Responses, &repb.BatchUpdateBlobsResponse_Response{
			Digest: r.Digest,
		})
	}
	return out, nil
}

func (f *fakeCAS) BatchReadBlobs(_ context.Context, req *repb.BatchReadBlobsRequest) (*repb.BatchReadBlobsResponse, error) {
	out := &repb.BatchReadBlobsResponse{}
	for _, d := range req.Digests {
		body, ok := f.blobs[d.Hash]
		if !ok {
			// Skip — caller checks Status.Code; simplest fake-fail
			// is omit. Tests should always pre-populate.
			continue
		}
		out.Responses = append(out.Responses, &repb.BatchReadBlobsResponse_Response{
			Digest: d,
			Data:   body,
		})
	}
	return out, nil
}

func (f *fakeCAS) Read(req *bspb.ReadRequest, srv bspb.ByteStream_ReadServer) error {
	// Resource name "blobs/<hash>/<size>" or
	// "<instance>/blobs/<hash>/<size>". Pull the hash out by
	// splitting on "blobs/" and taking the next segment.
	const sep = "blobs/"
	idx := strings.Index(req.ResourceName, sep)
	if idx < 0 {
		return nil
	}
	rest := req.ResourceName[idx+len(sep):]
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 1 {
		return nil
	}
	body, ok := f.blobs[parts[0]]
	if !ok {
		return nil
	}
	return srv.Send(&bspb.ReadResponse{Data: body})
}

// startFakeCAS spins up the fakeCAS on a loopback grpc listener
// and returns a connected CASClient + a teardown.
func startFakeCAS(t *testing.T, blobs map[string][]byte) (*CASClient, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	f := &fakeCAS{blobs: blobs}
	repb.RegisterContentAddressableStorageServer(srv, f)
	bspb.RegisterByteStreamServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		t.Fatal(err)
	}
	return NewCASClient(conn, ""), func() {
		_ = conn.Close()
		srv.Stop()
	}
}

// hashOf is the hex sha256 of b — what callers use as a CAS digest hash.
func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestCASClient_GetDirectory(t *testing.T) {
	// Build a synthetic CAS Directory: one file "hello.txt", one
	// subdir "lib".
	subDir := &repb.Directory{
		Files: []*repb.FileNode{{Name: "lib.h", Digest: &repb.Digest{Hash: "fake", SizeBytes: 0}}},
	}
	subBytes, err := proto.Marshal(subDir)
	if err != nil {
		t.Fatal(err)
	}
	subHash := hashOf(subBytes)

	root := &repb.Directory{
		Files: []*repb.FileNode{
			{Name: "hello.txt", Digest: &repb.Digest{Hash: "filehash", SizeBytes: 5}},
		},
		Directories: []*repb.DirectoryNode{
			{Name: "lib", Digest: &repb.Digest{Hash: subHash, SizeBytes: int64(len(subBytes))}},
		},
	}
	rootBytes, err := proto.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	rootHash := hashOf(rootBytes)

	client, teardown := startFakeCAS(t, map[string][]byte{
		rootHash:   rootBytes,
		subHash:    subBytes,
		"filehash": []byte("hello"),
	})
	defer teardown()

	got, err := client.GetDirectory(context.Background(), Digest{Hash: rootHash, Size: int64(len(rootBytes))})
	if err != nil {
		t.Fatalf("GetDirectory: %v", err)
	}
	if len(got.Files) != 1 || got.Files[0].Name != "hello.txt" {
		t.Errorf("unexpected files: %+v", got.Files)
	}
	if len(got.Directories) != 1 || got.Directories[0].Name != "lib" {
		t.Errorf("unexpected directories: %+v", got.Directories)
	}
}

func TestCASClient_ReadBlob(t *testing.T) {
	body := []byte("hello world")
	client, teardown := startFakeCAS(t, map[string][]byte{"filehash": body})
	defer teardown()

	got, err := client.ReadBlob(context.Background(), Digest{Hash: "filehash", Size: int64(len(body))})
	if err != nil {
		t.Fatalf("ReadBlob: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("got %q, want %q", got, body)
	}
}

func TestCASClient_PushBlob(t *testing.T) {
	client, teardown := startFakeCAS(t, map[string][]byte{})
	defer teardown()

	body := []byte("freshly pushed")
	d := Digest{Hash: "newhash", Size: int64(len(body))}
	if err := client.PushBlob(context.Background(), d, body); err != nil {
		t.Fatalf("PushBlob: %v", err)
	}
	// Verify by reading it back.
	got, err := client.ReadBlob(context.Background(), d)
	if err != nil {
		t.Fatalf("ReadBlob: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("got %q, want %q", got, body)
	}
}
