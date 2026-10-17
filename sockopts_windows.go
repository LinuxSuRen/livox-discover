//go:build windows

package main

import "syscall"

// controlReuseAddr is a no-op on Windows: SO_REUSEADDR there would allow
// hijacking the port from another socket, which is not what this tool wants.
func controlReuseAddr(network, address string, c syscall.RawConn) error {
	return nil
}
