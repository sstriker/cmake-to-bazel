//go:build linux

package main

import "github.com/hanwen/go-fuse/v2/fuse"

// serverShim is the platform-specific server handle the FUSE
// adapter returns. On Linux it's *fuse.Server; on other
// platforms it's a no-op pointer (Mount itself errors before
// any caller touches it).
type serverShim = *fuse.Server

func serverWait(s serverShim)          { s.Wait() }
func serverUnmount(s serverShim) error { return s.Unmount() }
