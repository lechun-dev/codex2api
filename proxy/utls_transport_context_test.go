package proxy

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	xproxy "golang.org/x/net/proxy"
)

type contextBlockingDialer struct {
	started chan struct{}
	release chan struct{}
}

func (d *contextBlockingDialer) Dial(network, address string) (net.Conn, error) {
	return nil, errors.New("legacy Dial should not be used")
}

func (d *contextBlockingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	close(d.started)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.release:
		return nil, errors.New("test dial released")
	}
}

var _ xproxy.Dialer = (*contextBlockingDialer)(nil)

func TestUTLSTransportDialCancellationDoesNotWaitForDialTimeout(t *testing.T) {
	dialer := &contextBlockingDialer{started: make(chan struct{}), release: make(chan struct{})}
	rt := &utlsRoundTripper{
		connections: make(map[string]*utlsConn),
		pending:     make(map[string]*utlsPendingConnection),
		dialer:      dialer,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	_, err := rt.getOrCreateConnection(ctx, "example.com", "example.com:443")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("getOrCreateConnection() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("canceled dial took %s; request cancellation was not propagated", elapsed)
	}
}

func TestUTLSTransportPendingConnectionWaitHonorsCancellation(t *testing.T) {
	dialer := &contextBlockingDialer{started: make(chan struct{}), release: make(chan struct{})}
	rt := &utlsRoundTripper{
		connections: make(map[string]*utlsConn),
		pending:     make(map[string]*utlsPendingConnection),
		dialer:      dialer,
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := rt.getOrCreateConnection(context.Background(), "example.com", "example.com:443")
		firstDone <- err
	}()
	select {
	case <-dialer.started:
	case <-time.After(time.Second):
		t.Fatal("first connection attempt did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	_, err := rt.getOrCreateConnection(ctx, "example.com", "example.com:443")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pending getOrCreateConnection() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("pending cancellation took %s; wait was not context-aware", elapsed)
	}

	close(dialer.release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first connection attempt did not finish")
	}
}
