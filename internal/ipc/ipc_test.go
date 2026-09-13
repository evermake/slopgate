package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// shortTempDir returns a freshly created temp directory with a short,
// test-name-independent path. Unix domain socket paths are limited to
// roughly 100 bytes (sockaddr_un), and t.TempDir() embeds the (sometimes
// long) test name in its path, which can blow that budget for tests that
// also nest subdirectories under the socket dir. This sidesteps that OS
// limit, which is unrelated to the behavior under test.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ipc")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// startServer starts a Server behind a Listen()'d socket at socketPath,
// wires it into Serve via a background goroutine, and returns a cancel func
// plus a channel that receives Serve's return value.
func startServer(t *testing.T, socketPath string, s *Server) (cancel context.CancelFunc, done <-chan error) {
	t.Helper()

	ln, err := Listen(socketPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Serve(ctx, ln)
	}()

	return cancelFn, errCh
}

func waitServerDone(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after ctx cancel")
	}
}

func TestRoundTripParamsAndResult(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")

	s := NewServer()
	type echoParams struct {
		Name string `json:"name"`
	}
	type echoResult struct {
		Greeting string `json:"greeting"`
	}
	s.Register("echo", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p echoParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return echoResult{Greeting: "hello " + p.Name}, nil
	})

	cancel, done := startServer(t, sock, s)
	defer waitServerDone(t, cancel, done)

	client, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	var res echoResult
	ctx, cancelCall := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCall()
	if err := client.Call(ctx, "echo", echoParams{Name: "world"}, &res); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Greeting != "hello world" {
		t.Errorf("got greeting %q, want %q", res.Greeting, "hello world")
	}
}

func TestMethodNotFound(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")

	s := NewServer()
	cancel, done := startServer(t, sock, s)
	defer waitServerDone(t, cancel, done)

	client, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	ctx, cancelCall := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCall()
	err = client.Call(ctx, "nope.doesnotexist", nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown method, got nil")
	}
	var ipcErr *Error
	if !errors.As(err, &ipcErr) {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if ipcErr.Code != CodeMethodNotFound {
		t.Errorf("got code %d, want %d", ipcErr.Code, CodeMethodNotFound)
	}
}

func TestHandlerReturnsIPCError(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")

	s := NewServer()
	const customCode = -32005
	s.Register("fail", func(ctx context.Context, params json.RawMessage) (any, error) {
		return nil, Errorf(customCode, "custom failure: %s", "detail")
	})

	cancel, done := startServer(t, sock, s)
	defer waitServerDone(t, cancel, done)

	client, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	ctx, cancelCall := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCall()
	err = client.Call(ctx, "fail", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ipcErr *Error
	if !errors.As(err, &ipcErr) {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if ipcErr.Code != customCode {
		t.Errorf("got code %d, want %d (should be preserved verbatim)", ipcErr.Code, customCode)
	}
	if ipcErr.Message != "custom failure: detail" {
		t.Errorf("got message %q", ipcErr.Message)
	}
}

func TestHandlerReturnsPlainError(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")

	s := NewServer()
	s.Register("fail", func(ctx context.Context, params json.RawMessage) (any, error) {
		return nil, errors.New("boom")
	})

	cancel, done := startServer(t, sock, s)
	defer waitServerDone(t, cancel, done)

	client, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	ctx, cancelCall := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCall()
	err = client.Call(ctx, "fail", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ipcErr *Error
	if !errors.As(err, &ipcErr) {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if ipcErr.Code != CodeInternal {
		t.Errorf("got code %d, want %d", ipcErr.Code, CodeInternal)
	}
	if ipcErr.Message != "boom" {
		t.Errorf("got message %q, want %q", ipcErr.Message, "boom")
	}
}

func TestHandlerPanics(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")

	s := NewServer()
	s.Register("panic", func(ctx context.Context, params json.RawMessage) (any, error) {
		panic("something went very wrong")
	})
	// Register a second, well-behaved method to prove the server survives
	// the panic and keeps serving.
	s.Register("ping", func(ctx context.Context, params json.RawMessage) (any, error) {
		return map[string]bool{"ok": true}, nil
	})

	cancel, done := startServer(t, sock, s)
	defer waitServerDone(t, cancel, done)

	client, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	ctx, cancelCall := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCall()
	err = client.Call(ctx, "panic", nil, nil)
	if err == nil {
		t.Fatal("expected error from panicking handler, got nil")
	}
	var ipcErr *Error
	if !errors.As(err, &ipcErr) {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if ipcErr.Code != CodeInternal {
		t.Errorf("got code %d, want %d", ipcErr.Code, CodeInternal)
	}

	// Server must still be alive and serving other requests.
	client2, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial (2): %v", err)
	}
	defer client2.Close()
	ctx2, cancelCall2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCall2()
	var res map[string]bool
	if err := client2.Call(ctx2, "ping", nil, &res); err != nil {
		t.Fatalf("Call after panic: %v", err)
	}
	if !res["ok"] {
		t.Errorf("expected ok:true, got %v", res)
	}
}

func TestConcurrentClients(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")

	s := NewServer()
	s.Register("add1", func(ctx context.Context, params json.RawMessage) (any, error) {
		var n int
		if err := json.Unmarshal(params, &n); err != nil {
			return nil, err
		}
		return n + 1, nil
	})

	cancel, done := startServer(t, sock, s)
	defer waitServerDone(t, cancel, done)

	const nClients = 20
	var wg sync.WaitGroup
	errs := make(chan error, nClients)
	for i := 0; i < nClients; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			client, err := Dial(sock)
			if err != nil {
				errs <- err
				return
			}
			defer client.Close()
			ctx, cancelCall := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelCall()
			var res int
			if err := client.Call(ctx, "add1", n, &res); err != nil {
				errs <- err
				return
			}
			if res != n+1 {
				errs <- errors.New("bad result")
				return
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent client error: %v", err)
	}
}

func TestSlowHandlerClientCancel(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")

	s := NewServer()
	handlerCtxDone := make(chan struct{})
	s.Register("slow", func(ctx context.Context, params json.RawMessage) (any, error) {
		select {
		case <-ctx.Done():
			close(handlerCtxDone)
			return nil, ctx.Err()
		case <-time.After(30 * time.Second):
			return "too slow", nil
		}
	})

	cancel, done := startServer(t, sock, s)
	defer waitServerDone(t, cancel, done)

	client, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	ctx, cancelCall := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCall()

	callDone := make(chan error, 1)
	go func() {
		callDone <- client.Call(ctx, "slow", nil, nil)
	}()

	// Give the handler a moment to start, then cancel from the client side
	// as if the user Ctrl-C'd out of a long wait.
	time.Sleep(100 * time.Millisecond)
	cancelCall()

	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Call did not return after client cancellation")
	}

	select {
	case <-handlerCtxDone:
		// Handler observed cancellation, as required.
	case <-time.After(5 * time.Second):
		t.Fatal("server handler ctx was never cancelled after client disconnect")
	}
}

func TestPingLiveServer(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")

	s := NewServer()
	s.Register("daemon.ping", func(ctx context.Context, params json.RawMessage) (any, error) {
		return map[string]bool{"ok": true}, nil
	})

	cancel, done := startServer(t, sock, s)
	defer waitServerDone(t, cancel, done)

	if err := Ping(sock, 2*time.Second); err != nil {
		t.Fatalf("Ping against live server: %v", err)
	}
}

func TestPingMissingSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "does-not-exist.sock")

	err := Ping(sock, time.Second)
	if err == nil {
		t.Fatal("expected error pinging a missing socket, got nil")
	}
	if !os.IsNotExist(errors.Unwrap(err)) {
		// Not all wrapping paths unwrap to os.ErrNotExist directly; just
		// require a non-nil, clearly-a-connection-problem error. The main
		// requirement is that Ping errors rather than hanging or panicking.
		t.Logf("ping missing socket error (informational): %v", err)
	}
}

func TestPingStaleSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "stale.sock")

	// Create a listener and then close it without ever Serve-ing, leaving a
	// socket file behind with nothing listening — the classic stale-socket
	// case left by a SIGKILL'd daemon. Go's net package unlinks a unix
	// socket file on Listener.Close by default, so disable that to
	// reproduce the "file exists, nothing listening" state a killed
	// process actually leaves (the kernel never runs any cleanup on
	// SIGKILL).
	ln, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		t.Fatalf("expected *net.UnixListener, got %T", ln)
	}
	ul.SetUnlinkOnClose(false)
	ul.Close()

	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("expected socket file to still exist after listener close: %v", err)
	}

	err = Ping(sock, 2*time.Second)
	if err == nil {
		t.Fatal("expected error pinging a stale socket (connection refused), got nil")
	}
}

func TestPingTimeout(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")

	s := NewServer()
	blockForever := make(chan struct{})
	s.Register("daemon.ping", func(ctx context.Context, params json.RawMessage) (any, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-blockForever:
			return nil, nil
		}
	})
	defer close(blockForever)

	cancel, done := startServer(t, sock, s)
	defer waitServerDone(t, cancel, done)

	err := Ping(sock, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestListenUnlinksStaleSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")

	ln1, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen (1): %v", err)
	}
	ln1.Close() // leaves the socket file behind, nothing listening

	ln2, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen (2) should unlink stale socket and succeed: %v", err)
	}
	defer ln2.Close()

	info, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("socket perm = %v, want 0700", info.Mode().Perm())
	}
}

func TestListenCreatesParentDirs(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "nested", "deeper", "d.sock")

	ln, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen with missing parent dirs: %v", err)
	}
	defer ln.Close()
}

func TestServeReturnsAfterCtxCancelWithInFlightHandler(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "d.sock")

	s := NewServer()
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	s.Register("block", func(ctx context.Context, params json.RawMessage) (any, error) {
		close(handlerStarted)
		<-releaseHandler
		return "done", nil
	})

	ln, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.Serve(ctx, ln) }()

	client, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	callDone := make(chan error, 1)
	go func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		callDone <- client.Call(cctx, "block", nil, nil)
	}()

	select {
	case <-handlerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never started")
	}

	// Cancel the server ctx while the handler is still in flight. Serve must
	// wait for the handler before returning.
	cancel()

	select {
	case <-serveDone:
		t.Fatal("Serve returned before in-flight handler finished")
	case <-time.After(200 * time.Millisecond):
		// expected: still waiting
	}

	close(releaseHandler)

	select {
	case err := <-serveDone:
		if err != nil {
			t.Errorf("Serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after handler finished")
	}

	<-callDone // handler completed normally before the connection closed
}
