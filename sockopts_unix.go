//go:build !windows

package main

import "syscall"

// controlReuseAddr sets SO_REUSEADDR so the tool can share the Livox ports
// with a driver for a quick peek without stopping it first.
func controlReuseAddr(network, address string, c syscall.RawConn) error {
	var opErr error
	if err := c.Control(func(fd uintptr) {
		opErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return opErr
}
