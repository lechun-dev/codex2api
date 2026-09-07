package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestDownstreamSSEKeepaliveFrameForRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		path       string
		userAgent  string
		originator string
		want       string
	}{
		{name: "codex v1 responses", path: "/v1/responses", userAgent: "codex-tui/0.142.0", want: downstreamSSEKeepaliveEvent},
		{name: "codex prefixless responses", path: "/responses", userAgent: "codex_cli_rs/0.128.0", want: downstreamSSEKeepaliveEvent},
		{name: "codex originator", path: "/v1/responses", originator: "codex-tui", want: downstreamSSEKeepaliveEvent},
		{name: "ordinary responses client", path: "/v1/responses", userAgent: "curl/8.0", want: downstreamSSEKeepaliveComment},
		{name: "codex chat completions", path: "/v1/chat/completions", userAgent: "codex-tui/0.142.0", want: downstreamSSEKeepaliveComment},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			c.Request.Header.Set("User-Agent", tc.userAgent)
			c.Request.Header.Set("Originator", tc.originator)
			if got := downstreamSSEKeepaliveFrameForRequest(c); got != tc.want {
				t.Fatalf("keepalive frame = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDownstreamSSEKeepaliveStopsAndJoins(t *testing.T) {
	var writes atomic.Int32
	firstWrite := make(chan struct{}, 1)
	stop := startDownstreamSSEKeepalive(context.Background(), time.Millisecond, func() bool {
		writes.Add(1)
		select {
		case firstWrite <- struct{}{}:
		default:
		}
		return true
	})

	select {
	case <-firstWrite:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("keepalive did not fire")
	}
	stop()
	stoppedAt := writes.Load()
	time.Sleep(10 * time.Millisecond)
	if got := writes.Load(); got != stoppedAt {
		t.Fatalf("keepalive wrote after stop returned: %d -> %d", stoppedAt, got)
	}
}

func TestDownstreamSSEKeepaliveStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var writes atomic.Int32
	firstWrite := make(chan struct{}, 1)
	stop := startDownstreamSSEKeepalive(ctx, time.Millisecond, func() bool {
		writes.Add(1)
		select {
		case firstWrite <- struct{}{}:
		default:
		}
		return true
	})
	defer stop()

	select {
	case <-firstWrite:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("keepalive did not fire")
	}
	cancel()
	stop()
	stoppedAt := writes.Load()
	time.Sleep(10 * time.Millisecond)
	if got := writes.Load(); got != stoppedAt {
		t.Fatalf("keepalive wrote after context cancellation: %d -> %d", stoppedAt, got)
	}
}

func TestDownstreamSSEKeepaliveStopsWhenWriterFails(t *testing.T) {
	var writes atomic.Int32
	firstWrite := make(chan struct{}, 1)
	stop := startDownstreamSSEKeepalive(context.Background(), time.Millisecond, func() bool {
		writes.Add(1)
		select {
		case firstWrite <- struct{}{}:
		default:
		}
		return false
	})
	defer stop()

	select {
	case <-firstWrite:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("keepalive did not fire")
	}
	stop()
	if got := writes.Load(); got != 1 {
		t.Fatalf("writer failure must stop keepalive after one write, got %d", got)
	}
}
