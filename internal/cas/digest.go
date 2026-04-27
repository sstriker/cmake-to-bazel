// Package cas provides a content-addressed store interface and
// REAPI-canonical digest / Merkle-tree helpers used by the M5 cache
// substrate.
//
// The Digest type is an alias for the upstream
// build.bazel.remote.execution.v2.Digest so cas-layer outputs feed
// straight into the gRPC client without re-shaping. Hashes are sha256;
// the wire format follows REAPI's canonical lower-case-hex convention.
package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/protobuf/proto"
)

// Digest is the REAPI Digest message. Alias avoids re-wrapping at
// every gRPC boundary.
type Digest = repb.Digest

// EmptyDigest is the digest of the zero-length blob — sha256 of "".
// REAPI requires this exact digest for empty files / empty directories.
func EmptyDigest() *Digest {
	return DigestOf(nil)
}

// DigestOf returns the Digest of a byte slice.
func DigestOf(b []byte) *Digest {
	sum := sha256.Sum256(b)
	return &Digest{
		Hash:      hex.EncodeToString(sum[:]),
		SizeBytes: int64(len(b)),
	}
}

// DigestProto serializes a proto message deterministically and returns
// its Digest. REAPI cache keys depend on byte-stable serialization, so
// callers must use this rather than ad-hoc encoding.
func DigestProto(m proto.Message) (*Digest, []byte, error) {
	body, err := MarshalDeterministic(m)
	if err != nil {
		return nil, nil, err
	}
	return DigestOf(body), body, nil
}

// MarshalDeterministic serializes a proto with field-tag-ordered
// encoding so two callers produce byte-identical bytes for equivalent
// messages. Required for stable digests.
func MarshalDeterministic(m proto.Message) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(m)
}

// DigestFile returns the Digest of a file's contents without buffering
// the whole file in memory.
func DigestFile(path string) (*Digest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return nil, fmt.Errorf("digest %s: %w", path, err)
	}
	return &Digest{
		Hash:      hex.EncodeToString(h.Sum(nil)),
		SizeBytes: n,
	}, nil
}

// DigestEqual reports whether two digests refer to the same blob.
func DigestEqual(a, b *Digest) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Hash == b.Hash && a.SizeBytes == b.SizeBytes
}

// DigestString renders a digest in the REAPI-conventional "hash/size"
// form used by bb_browser URLs and log lines.
func DigestString(d *Digest) string {
	if d == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s/%d", d.Hash, d.SizeBytes)
}
