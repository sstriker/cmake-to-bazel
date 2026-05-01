//go:build linux

package main

import "github.com/hanwen/go-fuse/v2/fuse"

func serverWait(s *fuse.Server)          { s.Wait() }
func serverUnmount(s *fuse.Server) error { return s.Unmount() }
