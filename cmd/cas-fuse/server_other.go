//go:build !linux

package main

func serverWait(_ *struct{})          {}
func serverUnmount(_ *struct{}) error { return nil }
