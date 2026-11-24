//go:build !darwin && !linux

package server

const tcpCloseSupported = false

func tcpPeerHasClosed(uintptr) bool { return false }
