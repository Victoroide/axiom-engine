package service

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"
)

// resourceLinkWork holds a permit until resolution, dialing and socket cleanup finish.
type resourceLinkWork struct {
	mu      sync.Mutex
	ended   bool
	active  chan struct{}
	release func()
}

func (w *resourceLinkWork) startDial(ctx context.Context) error {
	for {
		w.mu.Lock()
		if w.ended {
			w.mu.Unlock()
			return context.Canceled
		}
		if err := resourceLinkContextErr(ctx); err != nil {
			w.mu.Unlock()
			return err
		}
		previous := w.active
		if previous == nil {
			w.active = make(chan struct{})
			w.mu.Unlock()
			return nil
		}
		w.mu.Unlock()
		// The next redirect or fallback waits for the previous socket to close.
		select {
		case <-previous:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (w *resourceLinkWork) finishDial() {
	w.mu.Lock()
	defer w.mu.Unlock()
	close(w.active)
	w.active = nil
	if w.ended {
		w.release()
	}
}

func (w *resourceLinkWork) endCheck() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ended = true
	if w.active == nil {
		w.release()
	}
}

type resourceLinkConn struct {
	net.Conn
	closeSocket func() error
}

func (c resourceLinkConn) Close() error { return c.closeSocket() }

func (w *resourceLinkWork) trackSocket(ctx context.Context, conn net.Conn) net.Conn {
	tracked := resourceLinkConn{Conn: conn, closeSocket: sync.OnceValue(func() error {
		defer w.finishDial()
		return conn.Close()
	})}
	context.AfterFunc(ctx, func() { _ = tracked.Close() })
	return tracked
}

func dialResourceLink(ctx context.Context, dialer *net.Dialer, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return dialer.DialContext(ctx, network, address)
	}
	resolver := dialer.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	// The HTTP caller can stop waiting independently. Wait here for the system
	// resolver to finish so early cancellation cannot free the check's permit.
	addrs, err := resolver.LookupIPAddr(context.WithoutCancel(ctx), host)
	if err != nil {
		return nil, err
	}
	var firstErr error
	for i, ip := range addrs {
		if err := resourceLinkContextErr(ctx); err != nil {
			return nil, err
		}
		attempt, cancel := ctx, func() {}
		if deadline, ok := ctx.Deadline(); ok && i < len(addrs)-1 {
			// Divide the remaining time among addresses, with the same floor as net.Dialer.
			left := time.Until(deadline)
			budget := min(left, max(2*time.Second, left/time.Duration(len(addrs)-i)))
			attempt, cancel = context.WithTimeout(ctx, budget)
		}
		conn, err := dialer.DialContext(attempt, network, net.JoinHostPort(ip.String(), port))
		cancel()
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = errResourceLinkRefused
	}
	return nil, firstErr
}
