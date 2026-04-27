package regression

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
)

// sha256Wrapper bundles the std-lib hash with an extra HexSum() helper so
// per-element fingerprint computation reads naturally without leaking
// crypto/encoding boilerplate into the call site. Kept in its own file
// because load.go is otherwise schema-only.
type sha256Wrapper struct {
	hash.Hash
}

func newSHA256() *sha256Wrapper {
	return &sha256Wrapper{sha256.New()}
}

// HexSum returns the lowercase-hex sha256 digest with no separators.
func (h *sha256Wrapper) HexSum() string {
	return hex.EncodeToString(h.Sum(nil))
}

// _ keeps the io import live for any future Write-path helpers we slot
// in here. Removing the import to clean up vet is fine; this is a
// belt-and-suspenders pin.
var _ = io.Discard
