package wsrelay

import (
	"testing"
	"time"
)

func receiveTestValue[T any](t *testing.T, ch <-chan T, timeout time.Duration, description string) T {
	t.Helper()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case value, ok := <-ch:
		if !ok {
			t.Fatalf("%s channel closed without a value", description)
		}
		return value
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}

func waitForTestSignal(t *testing.T, ch <-chan struct{}, timeout time.Duration, description string) {
	t.Helper()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ch:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", description)
	}
}
