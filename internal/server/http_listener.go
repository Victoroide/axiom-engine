package server

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"

	"github.com/Victoroide/axiom-engine/internal/api"
)

type httpPeerKey struct{}

func newHTTPServer(app *fiber.App, drain context.Context) *http.Server {
	// Prepare the router once. The server handler also applies the application's
	// policy for HTTP methods Fiber cannot route.
	_ = app.Handler()
	fiberServer := app.Server()
	cfg := app.Config()
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	base := api.WithPeerCancellation(drain)
	bridge := &httpBridge{dispatch: fiberServer.Handler, reject: fiberServer.ErrorHandler, limit: int64(cfg.BodyLimit), logger: fiberServer.Logger}
	return &http.Server{
		Handler:                      bridge,
		ReadTimeout:                  cfg.ReadTimeout,
		WriteTimeout:                 cfg.WriteTimeout,
		IdleTimeout:                  cfg.IdleTimeout,
		MaxHeaderBytes:               4 * 1024,
		DisableGeneralOptionsHandler: true,
		Protocols:                    &protocols,
		BaseContext:                  func(net.Listener) context.Context { return base },
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			socket, _ := conn.(*httpReadConn)
			return context.WithValue(ctx, httpPeerKey{}, &httpPeer{local: conn.LocalAddr(), remote: conn.RemoteAddr(), socket: socket})
		},
	}
}

// httpPeer exposes socket metadata and the request context to Fiber. Fiber
// cannot read or write a socket that net/http may be sharing across streams.
type httpPeer struct {
	local, remote net.Addr
	incoming      context.Context
	socket        *httpReadConn
	rejection     int
}

func (p *httpPeer) requestContext() context.Context { return p.incoming }
func (p *httpPeer) requestRejection() int           { return p.rejection }
func (p *httpPeer) LocalAddr() net.Addr             { return p.local }
func (p *httpPeer) RemoteAddr() net.Addr            { return p.remote }
func (*httpPeer) Read([]byte) (int, error)          { return 0, io.ErrClosedPipe }
func (*httpPeer) Write([]byte) (int, error)         { return 0, io.ErrClosedPipe }
func (*httpPeer) Close() error                      { return io.ErrClosedPipe }
func (*httpPeer) SetDeadline(time.Time) error       { return io.ErrClosedPipe }
func (*httpPeer) SetReadDeadline(time.Time) error   { return io.ErrClosedPipe }
func (*httpPeer) SetWriteDeadline(time.Time) error {
	return io.ErrClosedPipe
}

type httpTLSPeer struct {
	*httpPeer
	state *tls.ConnectionState
}

func (*httpTLSPeer) Handshake() error                       { return nil }
func (p *httpTLSPeer) ConnectionState() tls.ConnectionState { return *p.state }

type httpBridge struct {
	dispatch fasthttp.RequestHandler
	reject   func(*fasthttp.RequestCtx, error)
	logger   fasthttp.Logger
	limit    int64
}

type httpExchange struct {
	ctx     fasthttp.RequestCtx
	peer    httpPeer
	tlsPeer httpTLSPeer
	buffer  [4096]byte
}

var httpExchanges = sync.Pool{New: func() any { return new(httpExchange) }}

func (h *httpBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	exchange := httpExchanges.Get().(*httpExchange)
	fctx := &exchange.ctx
	peer, _ := r.Context().Value(httpPeerKey{}).(*httpPeer)
	if peer != nil {
		exchange.peer = *peer
	}
	exchange.peer.incoming = r.Context()
	var conn net.Conn = &exchange.peer
	if r.TLS != nil {
		exchange.tlsPeer = httpTLSPeer{httpPeer: &exchange.peer, state: r.TLS}
		conn = &exchange.tlsPeer
	}
	fctx.Init2(conn, h.logger, true)
	defer func() {
		fctx.Request.Reset()
		fctx.Response.Reset()
		exchange.peer = httpPeer{}
		exchange.tlsPeer = httpTLSPeer{}
		httpExchanges.Put(exchange)
	}()

	// net/http moves chunked declarations to Trailer before the first body
	// read. With fixed-length framing, the raw header still holds the list.
	if declarations := r.Header["Trailer"]; r.ProtoMajor == 1 && (len(r.Trailer) != 0 || len(declarations) != 0) {
		if err := validateHTTP1Trailers(r.Trailer, declarations); err != nil {
			// Reject like a Fiber header error before copying request metadata.
			w.Header().Set("Connection", "close")
			h.reject(fctx, err)
			writeHTTPResponse(w, r.Method, &fctx.Response, exchange.buffer[:])
			return
		}
	}

	req := &fctx.Request
	req.Header.SetMethod(r.Method)
	target := r.RequestURI
	if !strings.HasPrefix(target, "/") {
		target = r.URL.RequestURI()
	}
	req.SetRequestURI(target)
	req.Header.SetHost(r.Host)
	switch {
	case r.ProtoMajor == 2:
		req.Header.SetProtocol("HTTP/2")
	case r.Proto != "HTTP/1.1":
		req.Header.SetProtocol(r.Proto)
	}
	for name, values := range r.Header {
		if name == "X-Forwarded-For" && len(values) > 1 {
			// Keep the proxy chain in wire order even if Fiber deletes a
			// different header and rearranges its header buffer.
			req.Header.Set(name, strings.Join(values, ", "))
			continue
		}
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	socket := exchange.peer.socket
	before := socket.readFailure()
	err := readHTTPBody(req, r, h.limit, exchange.buffer[:])
	if err == nil && r.ProtoMajor == 1 && r.ContentLength < 0 && len(r.Trailer) != 0 {
		err = validateHTTP1Trailers(r.Trailer, nil)
	}
	if err != nil {
		if r.ProtoMajor == 1 {
			// A rejected body can still be arriving. Send the error without
			// waiting for net/http to drain unread bytes for keep-alive.
			w.Header().Set("Connection", "close")
		}
		timedOut := httpBodyTimedOut(err)
		var trailerStatus int
		var socketErr error
		if r.ProtoMajor == 1 && r.ContentLength < 0 && !timedOut {
			socketErr = socket.readErrorSince(before)
			trailerStatus = http1TrailerStatus(err, socketErr)
		}
		if r.ContentLength <= h.limit && trailerStatus == 0 {
			// Fiber clears request metadata when payload parsing fails. The
			// declared length check and trailer parsing retain the headers.
			req.Reset()
		}
		timedOut = timedOut || trailerStatus == http.StatusRequestTimeout
		if timedOut || (trailerStatus != 0 && socketErr != nil) {
			// net/http cancels on socket read errors, including server
			// deadlines. Trailer error middleware needs a live context.
			exchange.peer.incoming = context.WithoutCancel(r.Context())
		}
		if timedOut {
			err = fasthttp.ErrTimeout
		}
		if trailerStatus == http.StatusRequestHeaderFieldsTooLarge {
			// Fiber can recognize its own buffer error type. Carry the
			// equivalent status from net/http through middleware replay.
			exchange.peer.rejection = trailerStatus
		}
		h.reject(fctx, err)
	} else {
		if len(r.Trailer.Values("X-Internal-Token")) != 0 || len(r.Trailer.Values("Authorization")) != 0 {
			fctx.SetUserValue(credentialTrailerKey{}, true)
			defer fctx.RemoveUserValue(credentialTrailerKey{})
		}
		if r.ProtoMajor == 1 {
			ctx, stop := socket.watchHTTP1Peer(r.Context())
			defer stop()
			exchange.peer.incoming = ctx
		}
		h.dispatch(fctx)
	}
	writeHTTPResponse(w, r.Method, &fctx.Response, exchange.buffer[:])
}

func readHTTPBody(req *fasthttp.Request, r *http.Request, limit int64, buffer []byte) error {
	if r.ContentLength > limit {
		return fasthttp.ErrBodyTooLarge
	}
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	// A known, small body can use the exchange buffer through dispatch. The
	// response writer can reuse the buffer once the middleware has returned.
	if r.ContentLength > 0 && r.ContentLength <= int64(len(buffer)) {
		body := buffer[:r.ContentLength]
		if _, err := io.ReadFull(r.Body, body); err != nil {
			return err
		}
		req.SetBodyRaw(body)
		req.Header.SetContentLength(len(body))
		return nil
	}
	maxRead := limit + 1
	if r.ContentLength > 0 {
		maxRead = r.ContentLength
	}
	body := io.LimitedReader{R: r.Body, N: maxRead}
	n, err := io.CopyBuffer(req.BodyWriter(), &body, buffer)
	if n > limit {
		return fasthttp.ErrBodyTooLarge
	}
	req.Header.SetContentLength(int(n))
	return err
}

func httpBodyTimedOut(err error) bool {
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

func validateHTTP1Trailers(trailers http.Header, declarations []string) error {
	// Use Fiber's field policy without moving any request headers into trailers.
	var fields fasthttp.RequestHeader
	for name := range trailers {
		if err := fields.SetTrailer(name); err != nil {
			return err
		}
	}
	for _, names := range declarations {
		if err := fields.SetTrailer(names); err != nil {
			return err
		}
	}
	return nil
}

// http1TrailerStatus classifies trailer parsing and validation errors.
// The chunk payload reader returns different framing errors and preserves a
// socket timeout. Trailer lookahead replaces a socket error with a framing
// error, so consult the underlying read before classifying a full buffer.
func http1TrailerStatus(err, socketErr error) int {
	if errors.Is(err, fasthttp.ErrBadTrailer) {
		return http.StatusBadRequest
	}
	switch err.Error() {
	case "http: unexpected EOF reading trailer", "http: suspiciously long trailer after chunked body":
		switch {
		case httpBodyTimedOut(socketErr):
			return http.StatusRequestTimeout
		case socketErr != nil || err.Error() == "http: unexpected EOF reading trailer":
			return http.StatusBadRequest
		default:
			return http.StatusRequestHeaderFieldsTooLarge
		}
	case "message too large":
		return http.StatusRequestHeaderFieldsTooLarge
	}
	var malformed textproto.ProtocolError
	if errors.As(err, &malformed) {
		return http.StatusBadRequest
	}
	return 0
}

func writeHTTPResponse(w http.ResponseWriter, method string, resp *fasthttp.Response, buffer []byte) {
	headers := w.Header()
	copyHTTPResponseHeaders(headers, &resp.Header)
	status := resp.StatusCode()
	if stream := resp.BodyStream(); stream != nil {
		defer func() { _ = resp.CloseBodyStream() }()
		w.WriteHeader(status)
		if method != http.MethodHead {
			writer := httpFlushWriter{ResponseWriter: w, controller: http.NewResponseController(w)}
			_, _ = io.CopyBuffer(writer, stream, buffer)
		}
		return
	}
	body := resp.Body()
	if status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified && headers.Get("Content-Length") == "" {
		headers.Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.WriteHeader(status)
	if method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func copyHTTPResponseHeaders(dst http.Header, src *fasthttp.ResponseHeader) {
	// net/http can use the headers after the Fiber response returns to
	// its pool. Copy the bytes once and give each header its own value slice.
	var encoded strings.Builder
	encoded.Grow(512)
	var offsets [24][2]int
	fields := offsets[:0]
	for key, value := range src.All() {
		if string(key) == "Transfer-Encoding" {
			continue
		}
		encoded.Write(key)
		keyEnd := encoded.Len()
		encoded.Write(value)
		fields = append(fields, [2]int{keyEnd, encoded.Len()})
	}
	data := encoded.String()
	values := make([]string, len(fields))
	start := 0
	for i, end := range fields {
		key := http.CanonicalHeaderKey(data[start:end[0]])
		value := data[end[0]:end[1]]
		if existing := dst[key]; len(existing) != 0 {
			dst[key] = append(existing, value)
		} else {
			values[i] = value
			dst[key] = values[i : i+1 : i+1]
		}
		start = end[1]
	}
}

type httpFlushWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
}

func (w httpFlushWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if err != nil {
		return n, err
	}
	return n, w.controller.Flush()
}
