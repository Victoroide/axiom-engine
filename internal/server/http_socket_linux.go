package server

import "golang.org/x/sys/unix"

const tcpCloseSupported = true

func tcpPeerHasClosed(fd uintptr) bool {
	// RawConn.Control supplies the kernel's signed integer file descriptor.
	poll := [1]unix.PollFd{{Fd: int32(fd), Events: unix.POLLRDHUP}} //nolint:gosec
	n, err := unix.Poll(poll[:], 0)
	return err == nil && n == 1 && poll[0].Revents&(unix.POLLRDHUP|unix.POLLHUP|unix.POLLERR) != 0
}
