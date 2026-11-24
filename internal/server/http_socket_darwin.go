package server

import "golang.org/x/sys/unix"

const tcpCloseSupported = true

func tcpPeerHasClosed(fd uintptr) bool {
	const established = 4 // TCPS_ESTABLISHED in netinet/tcp_fsm.h.
	info, err := unix.GetsockoptTCPConnectionInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_CONNECTION_INFO)
	return err == nil && info.State != established
}
