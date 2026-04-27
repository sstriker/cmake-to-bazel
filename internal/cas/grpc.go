package cas

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// GRPCStore implements Store against a REAPI-compatible gRPC endpoint
// (e.g. Buildbarn). Blobs under MaxBatchBlobSize round-trip via
// BatchUpdateBlobs/BatchReadBlobs; larger blobs use ByteStream Read/Write.
//
// The store is goroutine-safe; the underlying grpc.ClientConn handles
// connection multiplexing.
type GRPCStore struct {
	conn         *grpc.ClientConn
	cas          repb.ContentAddressableStorageClient
	ac           repb.ActionCacheClient
	bs           bytestream.ByteStreamClient
	instanceName string
	token        string

	// MaxBatchBlobSize is the largest blob (in bytes) sent through
	// BatchUpdate/BatchRead before falling back to ByteStream. Defaults
	// to 2 MiB on connect, well below the typical 4 MiB gRPC default
	// max_send_message_size (each batch entry plus protocol overhead
	// must fit in one message).
	MaxBatchBlobSize int
}

// GRPCConfig configures a GRPCStore. Endpoint is a "host:port" or full
// "grpc://host:port" / "grpcs://host:port" form; the scheme selects
// TLS. InstanceName is the REAPI instance prefix Buildbarn / RBE
// providers use for multi-tenancy.
type GRPCConfig struct {
	Endpoint     string
	InstanceName string

	// TLS options. When TLSCertFile is set, mTLS is used with the
	// provided client cert+key. When CAFile is set, that's the trust
	// root; otherwise the system roots are used. With Insecure=true,
	// these are ignored.
	Insecure    bool
	TLSCertFile string
	TLSKeyFile  string
	CAFile      string

	// TokenFile (optional): path to a file containing a bearer token
	// that's sent on every RPC as `authorization: Bearer <token>`.
	TokenFile string
}

// NewGRPCStore dials the REAPI endpoint and constructs a Store.
func NewGRPCStore(ctx context.Context, cfg GRPCConfig) (*GRPCStore, error) {
	endpoint, scheme := normalizeEndpoint(cfg.Endpoint)
	useTLS := !cfg.Insecure
	switch scheme {
	case "grpc":
		useTLS = false
	case "grpcs":
		useTLS = true
	}

	var dialOpts []grpc.DialOption
	if useTLS {
		tc, err := buildTLS(cfg)
		if err != nil {
			return nil, fmt.Errorf("cas grpc tls: %w", err)
		}
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tc)))
	} else {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.NewClient(endpoint, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("cas grpc dial %s: %w", endpoint, err)
	}

	s := &GRPCStore{
		conn:             conn,
		cas:              repb.NewContentAddressableStorageClient(conn),
		ac:               repb.NewActionCacheClient(conn),
		bs:               bytestream.NewByteStreamClient(conn),
		instanceName:     cfg.InstanceName,
		MaxBatchBlobSize: 2 * 1024 * 1024,
	}
	if cfg.TokenFile != "" {
		body, err := os.ReadFile(cfg.TokenFile)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("cas grpc token: %w", err)
		}
		s.token = strings.TrimSpace(string(body))
	}
	return s, nil
}

// Close releases the underlying gRPC connection. Safe to call once;
// subsequent calls return the same error.
func (s *GRPCStore) Close() error { return s.conn.Close() }

func normalizeEndpoint(raw string) (string, string) {
	switch {
	case strings.HasPrefix(raw, "grpc://"):
		return strings.TrimPrefix(raw, "grpc://"), "grpc"
	case strings.HasPrefix(raw, "grpcs://"):
		return strings.TrimPrefix(raw, "grpcs://"), "grpcs"
	default:
		return raw, ""
	}
}

func buildTLS(cfg GRPCConfig) (*tls.Config, error) {
	tc := &tls.Config{}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca %s: no certs parsed", cfg.CAFile)
		}
		tc.RootCAs = pool
	}
	if cfg.TLSCertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, err
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	return tc, nil
}

// withAuth attaches the bearer token (if any) to outbound RPC metadata.
func (s *GRPCStore) withAuth(ctx context.Context) context.Context {
	if s.token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+s.token)
}

// FindMissing implements Store.
func (s *GRPCStore) FindMissing(ctx context.Context, digests []*Digest) ([]*Digest, error) {
	if len(digests) == 0 {
		return nil, nil
	}
	resp, err := s.cas.FindMissingBlobs(s.withAuth(ctx), &repb.FindMissingBlobsRequest{
		InstanceName: s.instanceName,
		BlobDigests:  digests,
	})
	if err != nil {
		return nil, fmt.Errorf("cas FindMissingBlobs: %w", err)
	}
	return resp.MissingBlobDigests, nil
}

// GetBlob implements Store. Routes to BatchReadBlobs or ByteStream Read
// based on size.
func (s *GRPCStore) GetBlob(ctx context.Context, d *Digest) ([]byte, error) {
	if d.SizeBytes <= int64(s.MaxBatchBlobSize) {
		return s.batchRead(ctx, d)
	}
	return s.byteStreamRead(ctx, d)
}

func (s *GRPCStore) batchRead(ctx context.Context, d *Digest) ([]byte, error) {
	resp, err := s.cas.BatchReadBlobs(s.withAuth(ctx), &repb.BatchReadBlobsRequest{
		InstanceName: s.instanceName,
		Digests:      []*Digest{d},
	})
	if err != nil {
		return nil, fmt.Errorf("cas BatchReadBlobs %s: %w", DigestString(d), err)
	}
	if len(resp.Responses) != 1 {
		return nil, fmt.Errorf("cas BatchReadBlobs %s: expected 1 response, got %d",
			DigestString(d), len(resp.Responses))
	}
	r := resp.Responses[0]
	if r.Status != nil && r.Status.Code != 0 {
		if codes.Code(r.Status.Code) == codes.NotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("cas BatchReadBlobs %s: %s",
			DigestString(d), r.Status.Message)
	}
	return r.Data, nil
}

func (s *GRPCStore) byteStreamRead(ctx context.Context, d *Digest) ([]byte, error) {
	rn := s.readResourceName(d)
	stream, err := s.bs.Read(s.withAuth(ctx), &bytestream.ReadRequest{ResourceName: rn})
	if err != nil {
		return nil, fmt.Errorf("cas Read %s: %w", DigestString(d), err)
	}
	var buf bytes.Buffer
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("cas Read %s: %w", DigestString(d), err)
		}
		buf.Write(chunk.Data)
	}
	return buf.Bytes(), nil
}

// PutBlob implements Store.
func (s *GRPCStore) PutBlob(ctx context.Context, d *Digest, body []byte) error {
	got := DigestOf(body)
	if !DigestEqual(got, d) {
		return fmt.Errorf("putblob: declared %s but body digests to %s",
			DigestString(d), DigestString(got))
	}
	if int64(len(body)) <= int64(s.MaxBatchBlobSize) {
		return s.batchUpdate(ctx, d, body)
	}
	return s.byteStreamWrite(ctx, d, body)
}

func (s *GRPCStore) batchUpdate(ctx context.Context, d *Digest, body []byte) error {
	resp, err := s.cas.BatchUpdateBlobs(s.withAuth(ctx), &repb.BatchUpdateBlobsRequest{
		InstanceName: s.instanceName,
		Requests: []*repb.BatchUpdateBlobsRequest_Request{
			{Digest: d, Data: body},
		},
	})
	if err != nil {
		return fmt.Errorf("cas BatchUpdateBlobs %s: %w", DigestString(d), err)
	}
	if len(resp.Responses) != 1 {
		return fmt.Errorf("cas BatchUpdateBlobs %s: expected 1 response, got %d",
			DigestString(d), len(resp.Responses))
	}
	r := resp.Responses[0]
	if r.Status != nil && r.Status.Code != 0 {
		return fmt.Errorf("cas BatchUpdateBlobs %s: %s",
			DigestString(d), r.Status.Message)
	}
	return nil
}

func (s *GRPCStore) byteStreamWrite(ctx context.Context, d *Digest, body []byte) error {
	rn := s.writeResourceName(d)
	stream, err := s.bs.Write(s.withAuth(ctx))
	if err != nil {
		return fmt.Errorf("cas Write %s: %w", DigestString(d), err)
	}
	const chunk = 1024 * 1024
	offset := 0
	for offset < len(body) {
		end := offset + chunk
		if end > len(body) {
			end = len(body)
		}
		req := &bytestream.WriteRequest{
			ResourceName: rn,
			WriteOffset:  int64(offset),
			Data:         body[offset:end],
			FinishWrite:  end == len(body),
		}
		// Per ByteStream spec, ResourceName is only required on the
		// first chunk; sending it on every chunk is allowed and
		// simpler.
		if err := stream.Send(req); err != nil {
			return fmt.Errorf("cas Write %s: %w", DigestString(d), err)
		}
		offset = end
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		return fmt.Errorf("cas Write %s close: %w", DigestString(d), err)
	}
	return nil
}

// GetActionResult implements Store.
func (s *GRPCStore) GetActionResult(ctx context.Context, actionDigest *Digest) (*repb.ActionResult, error) {
	resp, err := s.ac.GetActionResult(s.withAuth(ctx), &repb.GetActionResultRequest{
		InstanceName: s.instanceName,
		ActionDigest: actionDigest,
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("ac GetActionResult %s: %w", DigestString(actionDigest), err)
	}
	return resp, nil
}

// UpdateActionResult implements Store.
func (s *GRPCStore) UpdateActionResult(ctx context.Context, actionDigest *Digest, ar *repb.ActionResult) error {
	_, err := s.ac.UpdateActionResult(s.withAuth(ctx), &repb.UpdateActionResultRequest{
		InstanceName: s.instanceName,
		ActionDigest: actionDigest,
		ActionResult: ar,
	})
	if err != nil {
		return fmt.Errorf("ac UpdateActionResult %s: %w", DigestString(actionDigest), err)
	}
	return nil
}

// readResourceName builds the ByteStream Read resource path:
//
//	{instance_name}/blobs/{hash}/{size_bytes}
//
// (or with a `/` prefix-strip when instance is empty).
func (s *GRPCStore) readResourceName(d *Digest) string {
	if s.instanceName == "" {
		return fmt.Sprintf("blobs/%s/%d", d.Hash, d.SizeBytes)
	}
	return fmt.Sprintf("%s/blobs/%s/%d", s.instanceName, d.Hash, d.SizeBytes)
}

// writeResourceName builds the ByteStream Write resource path with a
// fresh upload UUID:
//
//	{instance_name}/uploads/{uuid}/blobs/{hash}/{size_bytes}
func (s *GRPCStore) writeResourceName(d *Digest) string {
	id := uuid.NewString()
	if s.instanceName == "" {
		return fmt.Sprintf("uploads/%s/blobs/%s/%d", id, d.Hash, d.SizeBytes)
	}
	return fmt.Sprintf("%s/uploads/%s/blobs/%s/%d", s.instanceName, id, d.Hash, d.SizeBytes)
}
