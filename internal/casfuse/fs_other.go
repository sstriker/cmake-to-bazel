//go:build !linux

package casfuse

import "errors"

// MountOptions placeholder for cross-platform builds; the real
// definition lives in fs_linux.go. Keeping the type defined on
// every platform lets the CLI compile cross-platform; the mount
// path itself errors out at runtime.
type MountOptions struct {
	AllowOther bool
}

// Mount returns ErrNotImplemented on non-Linux platforms today.
// macOS NFSv4 support (per buildbarn's bb_clientd pattern) is a
// follow-up; tracked in docs/sources-design.md.
func Mount(_ *Tree, _ string, _ MountOptions) (*struct{}, error) {
	return nil, errors.New("casfuse.Mount: not implemented on this platform yet (Linux-only in v1)")
}

// MountRoot is the multi-digest counterpart to Mount; same
// non-Linux constraint applies in v1.
func MountRoot(_ *Root, _ string, _ MountOptions) (*struct{}, error) {
	return nil, errors.New("casfuse.MountRoot: not implemented on this platform yet (Linux-only in v1)")
}
