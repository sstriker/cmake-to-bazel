//go:build !linux

package main

type serverShim = *struct{}

func serverWait(_ serverShim)          {}
func serverUnmount(_ serverShim) error { return nil }
