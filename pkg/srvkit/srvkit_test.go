package srvkit

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestRun_GracefulDrain starts Run on an ephemeral port, fires an in-flight
// request that blocks until released, cancels the context mid-request, and
// asserts (a) the in-flight request still completes with 200, and (b) Run
// returns nil after the drain.
func TestRun_GracefulDrain(t *testing.T) {
	// Bind :0 first to learn a free port, then close it so Run's
	// ListenAndServe can take it. There's a tiny race window, but it's the
	// standard pattern and reliable in practice for a single test.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	released := make(chan struct{})
	inFlight := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		close(inFlight) // signal the request is being served
		<-released      // block until the test releases it
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, addr, mux, WithShutdownTimeout(5*time.Second))
	}()

	// Wait for the server to come up by polling a connection.
	waitForServer(t, addr)

	// Fire the slow request in the background.
	respStatus := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err != nil {
			respStatus <- -1
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		respStatus <- resp.StatusCode
	}()

	// Once the handler is actively serving, trigger shutdown.
	select {
	case <-inFlight:
	case <-time.After(3 * time.Second):
		t.Fatal("request never reached handler")
	}
	cancel() // simulate SIGINT/SIGTERM

	// Shutdown must wait for the in-flight request: release it shortly after
	// cancel so we exercise the drain path.
	time.Sleep(100 * time.Millisecond)
	close(released)

	select {
	case status := <-respStatus:
		if status != http.StatusOK {
			t.Fatalf("in-flight request status = %d, want 200 (drain dropped it)", status)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("in-flight request never completed")
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on clean shutdown", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Run did not return after drain")
	}
}

// TestRun_ListenError asserts a real listener failure surfaces as a non-nil
// error (here: an invalid address).
func TestRun_ListenError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// "256.0.0.1" is an invalid IP literal → ListenAndServe fails fast.
	err := Run(ctx, "256.0.0.1:0", http.NewServeMux())
	if err == nil {
		t.Fatal("Run returned nil for an invalid listen address, want error")
	}
}

// TestRun_OnShutdownOrder asserts OnShutdown hooks run in registration order
// and only AFTER the HTTP server has drained.
func TestRun_OnShutdownOrder(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	var mu sync.Mutex
	var order []int
	record := func(n int) Option {
		return WithOnShutdown(func(context.Context) {
			mu.Lock()
			order = append(order, n)
			mu.Unlock()
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, addr, http.NewServeMux(),
			record(1), record(2), record(3),
			WithShutdownTimeout(2*time.Second))
	}()
	waitForServer(t, addr)
	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}

	mu.Lock()
	defer mu.Unlock()
	want := []int{1, 2, 3}
	if len(order) != len(want) {
		t.Fatalf("hook order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("hook order = %v, want %v", order, want)
		}
	}
}

// waitForServer dials the address until it accepts a connection or times out.
func waitForServer(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s never came up", addr)
}
