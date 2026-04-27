package cas

import (
	"context"
	"errors"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
)

// ErrNotFound is returned from GetBlob and GetActionResult when the
// requested digest is not present in the store. Callers MUST treat
// missing blobs (even when an AC entry references them) as cache
// misses and re-execute, since AC entries can outlive their referenced
// blobs under aggressive CAS retention.
var ErrNotFound = errors.New("cas: not found")

// Store is the unified ContentAddressableStorage + ActionCache surface.
// Implementations include local.go (filesystem) for offline / tests
// and grpc.go (REAPI) for shared deployments.
//
// Methods are safe for concurrent use; implementations are responsible
// for any required locking.
type Store interface {
	// FindMissing returns the subset of digests NOT currently in CAS.
	// Used by upload paths to skip blobs that already exist remotely.
	// Order of the returned slice is implementation-defined.
	FindMissing(ctx context.Context, digests []*Digest) ([]*Digest, error)

	// GetBlob retrieves a blob's bytes by digest. Returns ErrNotFound
	// if absent. Callers SHOULD verify sha256(body) == d.Hash and
	// len(body) == d.SizeBytes; implementations may skip verification
	// for performance, but the caller is the trust boundary.
	GetBlob(ctx context.Context, d *Digest) ([]byte, error)

	// PutBlob stores a blob keyed by its digest. Re-storing an
	// already-present blob is a no-op (idempotent).
	PutBlob(ctx context.Context, d *Digest, body []byte) error

	// GetActionResult returns the cached ActionResult for an action
	// digest, or ErrNotFound. The result's referenced output blobs
	// MAY be missing from CAS (eviction); callers must validate
	// before trusting the entry.
	GetActionResult(ctx context.Context, actionDigest *Digest) (*repb.ActionResult, error)

	// UpdateActionResult publishes an ActionResult. Implementations
	// SHOULD overwrite any prior entry for the same action digest.
	UpdateActionResult(ctx context.Context, actionDigest *Digest, ar *repb.ActionResult) error
}
