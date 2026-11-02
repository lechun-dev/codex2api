package admin

import (
	"context"
	"errors"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 2026-11-02 coder(lq): Shared refreshes survive one canceled waiter and execute the expensive read once.
func TestDisplayStatsCoalescesAndSeparatesCancellation(t *testing.T) {
	var cache displayStatsCache
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	load := func(ctx context.Context) (int, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return 42, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, _, _, err := loadDisplayStats(&cache, ctx, "page", time.Second, load)
		first <- err
	}()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first waiter: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			value, _, stale, err := loadDisplayStats(&cache, context.Background(), "page", time.Second, load)
			if err != nil || value != 42 || stale {
				errs <- errors.New("shared load result differs")
			}
		})
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("loads=%d, want 1", calls.Load())
	}
}

func TestDisplayStatsStaleFailureBackoffAndMaximumAge(t *testing.T) {
	var cache displayStatsCache
	ctx := context.Background()
	if _, _, _, err := loadDisplayStats(&cache, ctx, "page", time.Second, func(context.Context) (int, error) { return 7, nil }); err != nil {
		t.Fatal(err)
	}
	cache.mu.Lock()
	entry := cache.entries["page"]
	entry.updatedAt = time.Now().Add(-time.Minute)
	cache.mu.Unlock()
	started, release := make(chan struct{}), make(chan struct{})
	failure := errors.New("database unavailable")
	load := func(context.Context) (int, error) {
		close(started)
		<-release
		return 0, failure
	}
	value, at, stale, err := loadDisplayStats(&cache, ctx, "page", time.Second, load)
	if err != nil || value != 7 || !stale || at.IsZero() {
		t.Fatalf("stale: value=%d stale=%t err=%v", value, stale, err)
	}
	<-started
	cache.mu.Lock()
	done := entry.done
	cache.mu.Unlock()
	close(release)
	<-done
	neverLoad := func(context.Context) (int, error) {
		t.Error("failure backoff ignored")
		return 0, failure
	}
	value, _, stale, err = loadDisplayStats(&cache, ctx, "page", time.Second, neverLoad)
	if err != nil || value != 7 || !stale {
		t.Fatalf("failure overwrote old data: %d %t %v", value, stale, err)
	}
	cache.mu.Lock()
	entry.updatedAt = time.Now().Add(-displayStatsMaxAge - time.Second)
	cache.mu.Unlock()
	_, _, _, err = loadDisplayStats(&cache, ctx, "page", time.Second, neverLoad)
	if !errors.Is(err, failure) {
		t.Fatalf("too-old data must not be returned: %v", err)
	}
}

func TestDisplayStatsColdFailureTimeoutAndInvalidation(t *testing.T) {
	var cache displayStatsCache
	_, _, _, err := loadDisplayStats(&cache, context.Background(), "slow", 10*time.Millisecond, func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cold timeout: %v", err)
	}
	_, _, _, err = loadDisplayStats(&cache, context.Background(), "slow", time.Second, func(context.Context) (int, error) {
		t.Error("cold failure should be throttled, not cached as zero")
		return 0, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cold failure lost: %v", err)
	}
	cache.clear()
	value, _, _, err := loadDisplayStats(&cache, context.Background(), "slow", time.Second, func(context.Context) (int, error) { return 9, nil })
	if err != nil || value != 9 {
		t.Fatalf("clear failed: %d %v", value, err)
	}
	// An in-flight refresh from before a clear cannot repopulate the new generation.
	started, release := make(chan struct{}), make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _, _, _ = loadDisplayStats(&cache, context.Background(), "old", time.Second, func(context.Context) (int, error) {
			close(started)
			<-release
			return 1, nil
		})
	}()
	<-started
	cache.clear()
	close(release)
	<-finished
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 0 {
		t.Fatal("invalidated refresh was installed")
	}
}

func TestDisplayStatsBoundedEntries(t *testing.T) {
	var cache displayStatsCache
	for i := 0; i < displayStatsMaxEntries+20; i++ {
		_, _, _, err := loadDisplayStats(&cache, context.Background(), time.Unix(int64(i), 0).String(), time.Second, func(context.Context) (int, error) { return 1, nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != displayStatsMaxEntries {
		t.Fatalf("entries=%d", len(cache.entries))
	}
}

func BenchmarkDisplayStatsCacheHit(b *testing.B) {
	var cache displayStatsCache
	load := func(context.Context) (int, error) { return 42, nil }
	_, _, _, _ = loadDisplayStats(&cache, context.Background(), "page", time.Second, load)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = loadDisplayStats(&cache, context.Background(), "page", time.Second, load)
	}
}

// 2026-11-02 coder(lq): Measure the real page-stats handler, keeping the same database and remaining uncached official/billing queries in both paths.
func BenchmarkAccountPageStatsDisplayCache(b *testing.B) {
	gin.SetMode(gin.TestMode)
	db, err := database.New("sqlite", filepath.Join(b.TempDir(), "page-stats.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	id, err := db.InsertAccount(ctx, "bench", "rt-bench", "")
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 4096; i++ {
		if err := db.InsertUsageLog(ctx, &database.UsageLogInput{AccountID: id, Endpoint: "/v1/responses", Model: "test-model", StatusCode: 200, TotalTokens: 10}); err != nil {
			b.Fatal(err)
		}
	}
	db.FlushUsageLogs()
	handler := &Handler{db: db}
	invoke := func() {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/accounts/page-stats?ids="+strconv.FormatInt(id, 10), nil)
		handler.GetAccountPageStats(c)
		if recorder.Code != http.StatusOK {
			b.Fatalf("page-stats: %d %s", recorder.Code, recorder.Body.String())
		}
	}
	for _, cached := range []bool{false, true} {
		name := "uncached"
		if cached {
			name = "cached"
		}
		b.Run(name, func(b *testing.B) {
			invoke()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !cached {
					handler.displayStats.clear()
				}
				invoke()
			}
		})
	}
}
