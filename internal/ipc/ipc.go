// Package ipc implements a minimal JSON-RPC 2.0 client and server over a
// Unix domain socket.
//
// The connection model is deliberately simple: one request, one response,
// then close. There is no multiplexing and no framing beyond a single JSON
// object per direction. This keeps long-blocking methods (a call that waits
// tens of minutes for some background work to finish) trivially correct,
// since the server handles each accepted connection in its own goroutine
// and that goroutine's lifetime is exactly the call's lifetime.
package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// JSON-RPC 2.0 error codes. The standard codes are reserved by the spec;
// the application codes below are specific to slopgate.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
	CodeNotFound       = -32000 // application: unknown repo or SHA
	CodeTimeout        = -32001 // application: run.wait deadline
)

// Error is a JSON-RPC error object. It implements the error interface, so a
// Handler may return one directly to control exactly what the client sees.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	return fmt.Sprintf("ipc: code %d: %s", e.Code, e.Message)
}

// Errorf builds an *Error with a formatted message.
func Errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// request is the wire format of a JSON-RPC request. id is always present and
// echoed back verbatim in the response, since the connection model is
// strictly one-request-one-response and id is not used to demultiplex.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is the wire format of a JSON-RPC response.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Handler processes one RPC call. Returning a *Error surfaces that exact
// code and message to the client; any other non-nil error is reported to
// the client as CodeInternal with the error's message text. A panic inside
// a Handler is recovered by the server and reported as CodeInternal.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Server dispatches JSON-RPC requests received over accepted connections to
// registered Handlers.
type Server struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

// NewServer returns an empty Server ready to have handlers registered on it.
func NewServer() *Server {
	return &Server{handlers: make(map[string]Handler)}
}

// Register associates method with h. Registering the same method twice
// replaces the previous handler.
func (s *Server) Register(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

func (s *Server) handler(method string) (Handler, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.handlers[method]
	return h, ok
}

// Serve accepts connections on ln, handling each in its own goroutine, until
// ctx is cancelled. It then closes ln, waits for in-flight handlers to
// finish, and returns nil. Closing the listener as a result of ctx
// cancellation is not reported as an error; any other Accept error is
// returned immediately (after in-flight handlers finish).
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	var wg sync.WaitGroup

	// Closing the listener unblocks Accept. We track whether that close was
	// requested by us (via ctx cancellation) so we can distinguish it from a
	// genuine Accept error.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = ln.Close()
		case <-done:
		}
	}()
	defer close(done)

	var acceptErr error
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				// Listener was closed because ctx was cancelled; not an error.
				acceptErr = nil
			default:
				acceptErr = err
			}
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleConn(ctx, conn)
		}()
	}

	wg.Wait()
	return acceptErr
}

// handleConn processes exactly one request/response cycle on conn, then
// closes it.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// Derive a ctx that is cancelled either when the server ctx is done or
	// when the peer disconnects (detected by a concurrent read attempt on
	// the connection, started once the request is fully read). This ensures
	// a client that gives up on a long call does not leave the handler
	// goroutine running forever.
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	dec := json.NewDecoder(conn)

	var req request
	if err := dec.Decode(&req); err != nil {
		s.writeResponse(conn, response{
			JSONRPC: "2.0",
			Error:   Errorf(CodeParse, "parse error: %v", err),
		})
		return
	}

	if req.JSONRPC != "2.0" || req.Method == "" {
		s.writeResponse(conn, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   Errorf(CodeInvalidRequest, "invalid request"),
		})
		return
	}

	h, ok := s.handler(req.Method)
	if !ok {
		s.writeResponse(conn, response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   Errorf(CodeMethodNotFound, "method not found: %s", req.Method),
		})
		return
	}

	// The request has been fully read and the protocol sends nothing more
	// from the client on this connection, so any further read either
	// observes the peer closing the connection (client gave up) or the
	// connection being closed locally once we're done (below). Either way
	// it is safe to start watching for disconnect now, without racing the
	// request decode above.
	peerGone := make(chan struct{})
	go func() {
		defer close(peerGone)
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	}()
	go func() {
		select {
		case <-peerGone:
			cancel()
		case <-connCtx.Done():
		}
	}()

	result, rpcErr := s.invoke(connCtx, h, req.Params)

	resp := response{JSONRPC: "2.0", ID: req.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			resp.Error = Errorf(CodeInternal, "marshal result: %v", err)
		} else {
			resp.Result = raw
		}
	}

	s.writeResponse(conn, resp)
}

// invoke calls h, recovering from a panic and normalizing any returned error
// into an *Error per the package's rules.
func (s *Server) invoke(ctx context.Context, h Handler, params json.RawMessage) (result any, rpcErr *Error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			rpcErr = Errorf(CodeInternal, "panic: %v", r)
		}
	}()

	res, err := h(ctx, params)
	if err != nil {
		var ipcErr *Error
		if errors.As(err, &ipcErr) {
			return nil, ipcErr
		}
		return nil, Errorf(CodeInternal, "%s", err.Error())
	}
	return res, nil
}

func (s *Server) writeResponse(conn net.Conn, resp response) {
	raw, err := json.Marshal(resp)
	if err != nil {
		// Should not happen; fall back to a minimal internal error.
		raw, _ = json.Marshal(response{
			JSONRPC: "2.0",
			ID:      resp.ID,
			Error:   Errorf(CodeInternal, "internal marshal error"),
		})
	}
	_, _ = conn.Write(raw)
}

// Listen creates a Unix domain socket listener at socketPath. It unlinks any
// stale socket file first (a daemon killed with SIGKILL leaves one behind),
// creates parent directories as needed, and restricts the socket to owner
// access (0700).
// maxSocketPath is the practical ceiling on a unix socket path. The kernel's
// sockaddr_un.sun_path is 104 bytes on darwin and 108 on linux, and exceeding
// it surfaces as a bare "bind: invalid argument" that explains nothing. Name
// the real cause instead.
const maxSocketPath = 100

func Listen(socketPath string) (net.Listener, error) {
	if len(socketPath) > maxSocketPath {
		return nil, fmt.Errorf("ipc: socket path is too long (%d bytes; the OS limit is ~%d): %s\n"+
			"set SLOPGATE_HOME to a shorter directory", len(socketPath), maxSocketPath, socketPath)
	}
	if dir := filepath.Dir(socketPath); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("ipc: create socket dir: %w", err)
		}
	}

	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("ipc: remove stale socket: %w", err)
	}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("ipc: listen: %w", err)
	}

	if err := os.Chmod(socketPath, 0o700); err != nil {
		ln.Close()
		return nil, fmt.Errorf("ipc: chmod socket: %w", err)
	}

	return ln, nil
}

// Client is a JSON-RPC client for a single Unix domain socket. Each Call
// dials, sends one request, reads one response, and closes the underlying
// connection.
type Client struct {
	socketPath string
}

// Dial returns a Client bound to socketPath. It does not connect
// immediately; Dial exists to validate the socket path and preserve room for
// future connection reuse, but the current implementation opens and closes
// a fresh connection per Call, consistent with the one-request-one-response
// model of the wire protocol.
func Dial(socketPath string) (*Client, error) {
	if socketPath == "" {
		return nil, errors.New("ipc: empty socket path")
	}
	return &Client{socketPath: socketPath}, nil
}

// Close releases any resources held by the Client. It is a no-op given the
// current per-Call connection model but is provided so callers can manage
// Client lifetime uniformly.
func (c *Client) Close() error {
	return nil
}

// Call invokes method on the server with the given params, blocking until a
// response is received or ctx is done. If result is non-nil, the response's
// result is unmarshalled into it. A server-side error, including one
// produced by ctx cancellation surfacing as a connection error, is returned
// as an error (an *Error when the server reported one).
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("ipc: dial: %w", err)
	}
	defer conn.Close()

	// Honour ctx cancellation on both write and read: apply the ctx
	// deadline (if any) to the connection, and additionally close the
	// connection if ctx is cancelled before the deadline / with no
	// deadline at all, which unblocks any in-flight Read/Write.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	cancelDone := make(chan struct{})
	defer close(cancelDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-cancelDone:
		}
	}()

	var paramsRaw json.RawMessage
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("ipc: marshal params: %w", err)
		}
		paramsRaw = raw
	}

	req := request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  paramsRaw,
	}

	reqRaw, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("ipc: marshal request: %w", err)
	}

	if _, err := conn.Write(reqRaw); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("ipc: write: %w", ctxErr)
		}
		return fmt.Errorf("ipc: write: %w", err)
	}

	// Deliberately do not half-close the write side here: the server treats
	// any further read activity on this connection past the request as a
	// signal that the peer has gone away (see handleConn), so half-closing
	// right after a normal request would look identical to that and cancel
	// the handler context immediately. json.Decoder on the server side does
	// not need an EOF to know a single JSON object is complete, so a full
	// write with no explicit termination is sufficient. We close the whole
	// connection only when done (below, via defer) or when ctx is
	// cancelled (via the goroutine above).
	respRaw, err := io.ReadAll(conn)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("ipc: read: %w", ctxErr)
		}
		return fmt.Errorf("ipc: read: %w", err)
	}

	var resp response
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		return fmt.Errorf("ipc: unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return resp.Error
	}

	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("ipc: unmarshal result: %w", err)
		}
	}

	return nil
}

// Ping is a convenience for callers (notably the git pre-receive hook) that
// only need to know whether a daemon is reachable. It dials socketPath,
// calls "daemon.ping", and closes the connection, all bounded by timeout. It
// returns a clear error when the socket file is absent, when it exists but
// nothing is listening (a stale socket left behind by a killed daemon), and
// on timeout.
func Ping(socketPath string, timeout time.Duration) error {
	if _, err := os.Stat(socketPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("ipc: socket does not exist: %s", socketPath)
		}
		return fmt.Errorf("ipc: stat socket: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client, err := Dial(socketPath)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Call(ctx, "daemon.ping", nil, nil); err != nil {
		return fmt.Errorf("ipc: ping: %w", err)
	}
	return nil
}
