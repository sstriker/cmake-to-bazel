package cas

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
)

// fakeCAS is an in-memory REAPI server covering CAS, ActionCache, and
// ByteStream — just enough to exercise GRPCStore end-to-end.
type fakeCAS struct {
	repb.UnimplementedContentAddressableStorageServer
	repb.UnimplementedActionCacheServer
	bytestream.UnimplementedByteStreamServer

	mu     sync.Mutex
	blobs  map[string][]byte
	action map[string]*repb.ActionResult
}

func newFakeCAS() *fakeCAS {
	return &fakeCAS{
		blobs:  make(map[string][]byte),
		action: make(map[string]*repb.ActionResult),
	}
}

func (f *fakeCAS) FindMissingBlobs(_ context.Context, req *repb.FindMissingBlobsRequest) (*repb.FindMissingBlobsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := &repb.FindMissingBlobsResponse{}
	for _, d := range req.BlobDigests {
		if _, ok := f.blobs[d.Hash]; !ok {
			resp.MissingBlobDigests = append(resp.MissingBlobDigests, d)
		}
	}
	return resp, nil
}

func (f *fakeCAS) BatchUpdateBlobs(_ context.Context, req *repb.BatchUpdateBlobsRequest) (*repb.BatchUpdateBlobsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := &repb.BatchUpdateBlobsResponse{}
	for _, r := range req.Requests {
		f.blobs[r.Digest.Hash] = append([]byte(nil), r.Data...)
		resp.Responses = append(resp.Responses, &repb.BatchUpdateBlobsResponse_Response{
			Digest: r.Digest,
		})
	}
	return resp, nil
}

func (f *fakeCAS) BatchReadBlobs(_ context.Context, req *repb.BatchReadBlobsRequest) (*repb.BatchReadBlobsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := &repb.BatchReadBlobsResponse{}
	for _, d := range req.Digests {
		body, ok := f.blobs[d.Hash]
		entry := &repb.BatchReadBlobsResponse_Response{Digest: d}
		if ok {
			entry.Data = body
		} else {
			entry.Status = &rpcstatus.Status{Code: int32(codes.NotFound)}
		}
		resp.Responses = append(resp.Responses, entry)
	}
	return resp, nil
}

func (f *fakeCAS) GetActionResult(_ context.Context, req *repb.GetActionResultRequest) (*repb.ActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ar, ok := f.action[req.ActionDigest.Hash]
	if !ok {
		return nil, status.Error(codes.NotFound, "no entry")
	}
	return proto.Clone(ar).(*repb.ActionResult), nil
}

func (f *fakeCAS) UpdateActionResult(_ context.Context, req *repb.UpdateActionResultRequest) (*repb.ActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.action[req.ActionDigest.Hash] = proto.Clone(req.ActionResult).(*repb.ActionResult)
	return req.ActionResult, nil
}

// Read serves ByteStream Read against the in-memory blob store.
// resource_name format: [instance/]blobs/{hash}/{size}
func (f *fakeCAS) Read(req *bytestream.ReadRequest, stream bytestream.ByteStream_ReadServer) error {
	hash, err := parseReadResource(req.ResourceName)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "%v", err)
	}
	f.mu.Lock()
	body, ok := f.blobs[hash]
	f.mu.Unlock()
	if !ok {
		return status.Errorf(codes.NotFound, "blob %s missing", hash)
	}
	const chunk = 64 * 1024
	for off := int64(0); off < int64(len(body)); off += chunk {
		end := off + chunk
		if end > int64(len(body)) {
			end = int64(len(body))
		}
		if err := stream.Send(&bytestream.ReadResponse{Data: body[off:end]}); err != nil {
			return err
		}
	}
	return nil
}

// Write serves ByteStream Write. resource_name format:
// [instance/]uploads/{uuid}/blobs/{hash}/{size}
func (f *fakeCAS) Write(stream bytestream.ByteStream_WriteServer) error {
	var (
		hash string
		buf  bytes.Buffer
	)
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if hash == "" && req.ResourceName != "" {
			h, err := parseWriteResource(req.ResourceName)
			if err != nil {
				return status.Errorf(codes.InvalidArgument, "%v", err)
			}
			hash = h
		}
		buf.Write(req.Data)
		if req.FinishWrite {
			break
		}
	}
	if hash == "" {
		return status.Error(codes.InvalidArgument, "resource_name never set")
	}
	body := buf.Bytes()
	f.mu.Lock()
	f.blobs[hash] = body
	f.mu.Unlock()
	return stream.SendAndClose(&bytestream.WriteResponse{CommittedSize: int64(len(body))})
}

func parseReadResource(rn string) (string, error) {
	parts := strings.Split(rn, "/")
	for i, p := range parts {
		if p == "blobs" && i+2 < len(parts) {
			if _, err := strconv.ParseInt(parts[i+2], 10, 64); err != nil {
				return "", err
			}
			return parts[i+1], nil
		}
	}
	return "", errors.New("missing blobs/ segment in resource_name")
}

func parseWriteResource(rn string) (string, error) {
	parts := strings.Split(rn, "/")
	for i, p := range parts {
		if p == "uploads" && i+1 < len(parts) {
			rest := parts[i+2:]
			for j, q := range rest {
				if q == "blobs" && j+2 < len(rest) {
					return rest[j+1], nil
				}
			}
		}
	}
	return "", errors.New("missing uploads/.../blobs/ segment in resource_name")
}

// startFakeCAS spins up an in-process gRPC server and returns a connected
// GRPCStore plus a teardown.
func startFakeCAS(t *testing.T) (*GRPCStore, *fakeCAS, func()) {
	t.Helper()
	fake := newFakeCAS()
	srv := grpc.NewServer()
	repb.RegisterContentAddressableStorageServer(srv, fake)
	repb.RegisterActionCacheServer(srv, fake)
	bytestream.RegisterByteStreamServer(srv, fake)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	s := &GRPCStore{
		conn:             conn,
		cas:              repb.NewContentAddressableStorageClient(conn),
		ac:               repb.NewActionCacheClient(conn),
		bs:               bytestream.NewByteStreamClient(conn),
		MaxBatchBlobSize: 64, // force ByteStream for blobs > 64 bytes
	}
	teardown := func() {
		conn.Close()
		srv.Stop()
		lis.Close()
	}
	return s, fake, teardown
}

func TestGRPCStore_BatchRoundTrip(t *testing.T) {
	s, _, teardown := startFakeCAS(t)
	defer teardown()
	ctx := context.Background()
	body := []byte("small")
	d := DigestOf(body)

	if err := s.PutBlob(ctx, d, body); err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	got, err := s.GetBlob(ctx, d)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("body mismatch: got %q want %q", got, body)
	}
}

func TestGRPCStore_ByteStreamRoundTrip(t *testing.T) {
	s, _, teardown := startFakeCAS(t)
	defer teardown()
	ctx := context.Background()
	body := bytes.Repeat([]byte("X"), 1024) // > MaxBatchBlobSize=64
	d := DigestOf(body)

	if err := s.PutBlob(ctx, d, body); err != nil {
		t.Fatalf("PutBlob (stream): %v", err)
	}
	got, err := s.GetBlob(ctx, d)
	if err != nil {
		t.Fatalf("GetBlob (stream): %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("stream body mismatch: got %d bytes want %d", len(got), len(body))
	}
}

func TestGRPCStore_FindMissing(t *testing.T) {
	s, _, teardown := startFakeCAS(t)
	defer teardown()
	ctx := context.Background()

	present := []byte("here")
	pd := DigestOf(present)
	if err := s.PutBlob(ctx, pd, present); err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	absent := DigestOf([]byte("not stored"))

	missing, err := s.FindMissing(ctx, []*Digest{pd, absent})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if len(missing) != 1 || !DigestEqual(missing[0], absent) {
		t.Errorf("expected [%s], got %v", DigestString(absent), missing)
	}
}

func TestGRPCStore_GetBlob_NotFound(t *testing.T) {
	s, _, teardown := startFakeCAS(t)
	defer teardown()
	_, err := s.GetBlob(context.Background(), DigestOf([]byte("never put")))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGRPCStore_ActionResultRoundTrip(t *testing.T) {
	s, _, teardown := startFakeCAS(t)
	defer teardown()
	ctx := context.Background()

	ad := DigestOf([]byte("action"))
	want := &repb.ActionResult{
		ExitCode: 0,
		OutputFiles: []*repb.OutputFile{
			{Path: "out/BUILD.bazel", Digest: DigestOf([]byte("BUILD"))},
		},
	}
	if err := s.UpdateActionResult(ctx, ad, want); err != nil {
		t.Fatalf("UpdateActionResult: %v", err)
	}
	got, err := s.GetActionResult(ctx, ad)
	if err != nil {
		t.Fatalf("GetActionResult: %v", err)
	}
	if got.ExitCode != want.ExitCode {
		t.Errorf("exit_code: got %d want %d", got.ExitCode, want.ExitCode)
	}
	if len(got.OutputFiles) != 1 || got.OutputFiles[0].Path != "out/BUILD.bazel" {
		t.Errorf("output_files: %+v", got.OutputFiles)
	}
}

func TestGRPCStore_GetActionResult_NotFound(t *testing.T) {
	s, _, teardown := startFakeCAS(t)
	defer teardown()
	_, err := s.GetActionResult(context.Background(), DigestOf([]byte("none")))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
