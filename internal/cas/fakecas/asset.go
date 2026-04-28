package fakecas

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	raa "github.com/bazelbuild/remote-apis/build/bazel/remote/asset/v1"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
)

// AssetServer implements the REAPI Remote Asset Fetch + Push services
// against an in-memory map. Used by tests that exercise the M3d
// source-CAS resolver without a real BuildStream / Buildbarn endpoint.
//
// Lookup keys are (uri + sorted qualifiers); two pushes that disagree
// on qualifier order map to the same entry.
type AssetServer struct {
	raa.UnimplementedFetchServer
	raa.UnimplementedPushServer

	mu          sync.Mutex
	directory   map[string]*repb.Digest
	directories map[string]*assetEntry // for retrieving qualifiers on Fetch
}

type assetEntry struct {
	digest     *repb.Digest
	qualifiers []*raa.Qualifier
}

// NewAssetServer returns an empty asset server.
func NewAssetServer() *AssetServer {
	return &AssetServer{
		directory:   make(map[string]*repb.Digest),
		directories: make(map[string]*assetEntry),
	}
}

// AssetCount reports the number of asset entries currently held.
func (s *AssetServer) AssetCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.directory)
}

// FetchDirectory implements the REAPI service.
func (s *AssetServer) FetchDirectory(_ context.Context, req *raa.FetchDirectoryRequest) (*raa.FetchDirectoryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, uri := range req.Uris {
		key := assetKey(uri, req.Qualifiers)
		if entry, ok := s.directories[key]; ok {
			return &raa.FetchDirectoryResponse{
				Uri:                 uri,
				Qualifiers:          entry.qualifiers,
				RootDirectoryDigest: entry.digest,
			}, nil
		}
	}
	return &raa.FetchDirectoryResponse{
		Status: &rpcstatus.Status{
			Code:    5, // NOT_FOUND
			Message: "asset not found",
		},
	}, nil
}

// PushDirectory implements the REAPI service.
func (s *AssetServer) PushDirectory(_ context.Context, req *raa.PushDirectoryRequest) (*raa.PushDirectoryResponse, error) {
	if req.RootDirectoryDigest == nil {
		return nil, errors.New("PushDirectory: root_directory_digest required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, uri := range req.Uris {
		key := assetKey(uri, req.Qualifiers)
		s.directory[key] = req.RootDirectoryDigest
		s.directories[key] = &assetEntry{
			digest:     req.RootDirectoryDigest,
			qualifiers: req.Qualifiers,
		}
	}
	return &raa.PushDirectoryResponse{}, nil
}

// assetKey concatenates uri + sorted "name=value" qualifier pairs into
// a single lookup string so caller-side ordering doesn't fragment the
// cache.
func assetKey(uri string, qualifiers []*raa.Qualifier) string {
	parts := make([]string, 0, len(qualifiers))
	for _, q := range qualifiers {
		parts = append(parts, q.Name+"="+q.Value)
	}
	sort.Strings(parts)
	return uri + "\x00" + strings.Join(parts, "\x00")
}
