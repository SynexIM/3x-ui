package xray

import (
	"context"
	"net"
	"testing"
	"time"

	statsService "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
)

// Init is what the panel used to treat as proof that the core was up. It is
// not: grpc.NewClient never dials, so Init succeeds against a port nothing is
// listening on. This is the whole reason a node could report a successful
// restart while its core was already dead.
func TestInitSucceedsAgainstADeadPort(t *testing.T) {
	port := closedPort(t)

	api := &XrayAPI{}
	if err := api.Init(port); err != nil {
		t.Fatalf("Init unexpectedly failed on a dead port: %v", err)
	}
	api.Close()
}

// WaitForAPIReady must not return until something actually answers, so a caller
// that gets nil back knows the core accepted its config and started serving.
func TestWaitForAPIReadyFailsWhileNothingListens(t *testing.T) {
	port := closedPort(t)

	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()

	if err := WaitForAPIReady(ctx, port); err == nil {
		t.Fatal("WaitForAPIReady reported a dead port as ready")
	}
}

// A plain TCP listener accepts the connection but never speaks gRPC. Readiness
// must not be satisfied by the accept alone, otherwise any process holding the
// port would pass for a serving core.
func TestWaitForAPIReadyRejectsANonGrpcListener(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			// Hold the connection open without ever answering.
			t.Cleanup(func() { conn.Close() })
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := WaitForAPIReady(ctx, lis.Addr().(*net.TCPAddr).Port); err == nil {
		t.Fatal("a socket that never speaks gRPC was reported ready")
	}
}

// The positive case: a real gRPC server on the API port is what a started core
// looks like, and readiness must be reached well inside the startup budget.
func TestWaitForAPIReadySucceedsAgainstAServingCore(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	statsService.RegisterStatsServiceServer(srv, &fakeStatsServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := WaitForAPIReady(ctx, lis.Addr().(*net.TCPAddr).Port); err != nil {
		t.Fatalf("a serving core was not recognised as ready: %v", err)
	}
}

// WaitForAPIReady must return as soon as its context is cancelled, which is how
// Start stops waiting the moment the core process exits instead of burning the
// full startup timeout on a port that will never open.
func TestWaitForAPIReadyReturnsWhenCancelled(t *testing.T) {
	port := closedPort(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- WaitForAPIReady(ctx, port) }()

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation must not be reported as ready")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForAPIReady ignored its cancelled context")
	}
}

// closedPort returns a port that was bound and released, so nothing is
// listening on it.
func closedPort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	lis.Close()
	return port
}
