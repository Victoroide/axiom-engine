package server

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"time"
)

type httpReadListener struct{ net.Listener }

func (l httpReadListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		return &httpReadConn{TCPConn: tcp}, nil
	}
	return conn, nil
}

type httpReadConn struct {
	*net.TCPConn
	lastFailure atomic.Pointer[httpReadFailure]
}

type httpReadFailure struct{ err error }

func (c *httpReadConn) Read(p []byte) (int, error) {
	n, err := c.TCPConn.Read(p)
	if err != nil {
		c.lastFailure.Store(&httpReadFailure{err: err})
	}
	return n, err
}

func (c *httpReadConn) readFailure() *httpReadFailure {
	if c == nil {
		return nil
	}
	return c.lastFailure.Load()
}

func (c *httpReadConn) readErrorSince(before *httpReadFailure) error {
	if after := c.readFailure(); after != nil && after != before {
		return after.err
	}
	return nil
}

var errResponseFinished = errors.New("HTTP response finished")

// watchHTTP1Peer observes TCP shutdown without consuming pipelined bytes.
// net/http may stop its background reads after buffering a pipelined byte.
func (c *httpReadConn) watchHTTP1Peer(parent context.Context) (context.Context, func()) {
	if c == nil || !tcpCloseSupported {
		return parent, func() {}
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return parent, func() {}
	}
	ctx, cancel := context.WithCancelCause(parent)
	const interval = 20 * time.Millisecond
	timer := time.AfterFunc(interval, func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for ctx.Err() == nil {
			var closed bool
			err := raw.Control(func(fd uintptr) { closed = tcpPeerHasClosed(fd) })
			if err != nil || closed {
				cancel(context.Canceled)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
	return ctx, func() {
		timer.Stop()
		cancel(errResponseFinished)
	}
}
