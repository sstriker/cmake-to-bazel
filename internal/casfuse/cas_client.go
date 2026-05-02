// Package casfuse implements a FUSE filesystem that serves the
// contents of a REAPI ContentAddressableStorage Directory by
// digest. Used by cmd/cas-fuse to mount source trees that
// `bst source push` deposited into CAS, so Bazel repo rules can
// reference them as on-disk paths without dev machines having to
// download the bytes durably.
//
// Architecture (per docs/sources-design.md, "Consumption: FUSE
// daemon" section):
//
//   - cas_client.go: thin gRPC wrapper around REAPI CAS RPCs.
//     GetDirectory + ReadBlob are the only ones the FS needs.
//   - directory.go / file.go: backend-agnostic virtual nodes that
//     model a CAS Directory tree. Testable without an actual mount.
//   - fs_linux.go: go-fuse adapter (Linux build-tag).
//   - cmd/cas-fuse: CLI that wires it together.
//
// The package is laid out so the CAS client and virtual nodes
// build (and test) on every platform; only the FUSE mount layer
// is Linux-gated. macOS NFSv4 support is a follow-up that can
// land alongside its own build-tagged sibling.
package casfuse

import (
	"context"
	"fmt"
	"io"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// Digest is the local mirror of repb.Digest with simple parsing
// helpers. Wire format is "<hex-hash>-<size>" (matches buildbarn's
// path encoding under <instance>/blobs/directory/<hash>-<size>/).
type Digest struct {
	Hash string
	Size int64
}

// ParseDigest splits "<hash>-<size>" into a Digest.
func ParseDigest(s string) (Digest, error) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '-' {
			var size int64
			if _, err := fmt.Sscanf(s[i+1:], "%d", &size); err != nil {
				return Digest{}, fmt.Errorf("parse digest size %q: %w", s[i+1:], err)
			}
			return Digest{Hash: s[:i], Size: size}, nil
		}
	}
	return Digest{}, fmt.Errorf("digest %q missing '-' separator", s)
}

// String renders Digest in the wire format ParseDigest accepts.
func (d Digest) String() string { return fmt.Sprintf("%s-%d", d.Hash, d.Size) }

// toProto converts to repb.Digest for the gRPC calls.
func (d Digest) toProto() *repb.Digest {
	return &repb.Digest{Hash: d.Hash, SizeBytes: d.Size}
}

// CASClient is a minimal REAPI CAS client. The "instance name"
// is the REAPI logical-instance string (often "" or "main"); we
// pass it through on every call so a single client can serve
// multiple instances if the daemon ever needs to.
type CASClient struct {
	cas      repb.ContentAddressableStorageClient
	bs       bspb.ByteStreamClient
	instance string
}

// NewCASClient wraps an existing gRPC client connection.
// Caller owns the conn (close it when done).
func NewCASClient(conn *grpc.ClientConn, instance string) *CASClient {
	return &CASClient{
		cas:      repb.NewContentAddressableStorageClient(conn),
		bs:       bspb.NewByteStreamClient(conn),
		instance: instance,
	}
}

// GetDirectory fetches a CAS Directory proto by digest. Used to
// populate the virtual FS's directory listings.
func (c *CASClient) GetDirectory(ctx context.Context, d Digest) (*repb.Directory, error) {
	// REAPI offers BatchReadBlobs (cheap for small blobs) and
	// GetTree (returns the Directory + its descendants in one
	// streaming call). For lazy directory traversal we just want
	// the single Directory; BatchReadBlobs fits.
	resp, err := c.cas.BatchReadBlobs(ctx, &repb.BatchReadBlobsRequest{
		InstanceName: c.instance,
		Digests:      []*repb.Digest{d.toProto()},
	})
	if err != nil {
		return nil, fmt.Errorf("BatchReadBlobs(%s): %w", d, err)
	}
	if len(resp.Responses) != 1 {
		return nil, fmt.Errorf("BatchReadBlobs(%s) returned %d responses, want 1", d, len(resp.Responses))
	}
	r := resp.Responses[0]
	if r.Status != nil && r.Status.Code != 0 {
		return nil, fmt.Errorf("BatchReadBlobs(%s): %s", d, r.Status.Message)
	}
	dir := &repb.Directory{}
	if err := proto.Unmarshal(r.Data, dir); err != nil {
		return nil, fmt.Errorf("unmarshal Directory(%s): %w", d, err)
	}
	return dir, nil
}

// PushBlob uploads bytes for a single blob to CAS. Used by
// cmd/source-push to populate a CAS instance from a packed
// source tree without going through BuildStream — handy for
// tests and dev workflows where bst isn't installed.
//
// Uses BatchUpdateBlobs for small payloads (fits in one RPC).
// Caller is responsible for chunking large blobs across calls;
// source files are typically small enough that this isn't a
// concern in practice.
func (c *CASClient) PushBlob(ctx context.Context, d Digest, body []byte) error {
	resp, err := c.cas.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		InstanceName: c.instance,
		Requests: []*repb.BatchUpdateBlobsRequest_Request{
			{Digest: d.toProto(), Data: body},
		},
	})
	if err != nil {
		return fmt.Errorf("BatchUpdateBlobs(%s): %w", d, err)
	}
	if len(resp.Responses) != 1 {
		return fmt.Errorf("BatchUpdateBlobs(%s) returned %d responses, want 1", d, len(resp.Responses))
	}
	r := resp.Responses[0]
	if r.Status != nil && r.Status.Code != 0 {
		return fmt.Errorf("BatchUpdateBlobs(%s): %s", d, r.Status.Message)
	}
	return nil
}

// ReadBlob streams a blob's contents into a buffer and returns
// it. The daemon could stream into the FUSE read response
// directly for big files; v1 keeps the implementation simple by
// buffering — the read paths narrowing pass keeps source files
// individually small enough that this is fine.
func (c *CASClient) ReadBlob(ctx context.Context, d Digest) ([]byte, error) {
	resourceName := c.instance
	if resourceName != "" {
		resourceName += "/"
	}
	resourceName += fmt.Sprintf("blobs/%s/%d", d.Hash, d.Size)
	stream, err := c.bs.Read(ctx, &bspb.ReadRequest{ResourceName: resourceName})
	if err != nil {
		return nil, fmt.Errorf("ByteStream.Read(%s): %w", d, err)
	}
	var buf []byte
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			return buf, nil
		}
		if err != nil {
			return nil, fmt.Errorf("ByteStream.Read(%s) recv: %w", d, err)
		}
		buf = append(buf, chunk.Data...)
	}
}
