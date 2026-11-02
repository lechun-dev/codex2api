package admin

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	displayStatsTTL        = 30 * time.Second
	displayStatsMaxAge     = 5 * time.Minute
	displayStatsRetryDelay = 5 * time.Second
	displayStatsMaxEntries = 64
)

type displayStatsCache struct {
	mu      sync.Mutex
	entries map[string]*displayStatsEntry
}

type displayStatsEntry struct {
	value     interface{}
	updatedAt time.Time
	retryAt   time.Time
	done      chan struct{}
	err       error
}

// 2026-11-02 coder(lq): Display-only, bounded stale-while-revalidate cache. Never use it for authentication, charging or quota enforcement.
// Values are immutable after installation; callers must not mutate returned maps.
func loadDisplayStats[T any](cache *displayStatsCache, ctx context.Context, key string, timeout time.Duration, load func(context.Context) (T, error)) (value T, updatedAt time.Time, stale bool, err error) {
	if err := ctx.Err(); err != nil {
		return value, updatedAt, false, err
	}
	cache.mu.Lock()
	if cache.entries == nil {
		cache.entries = make(map[string]*displayStatsEntry)
	}
	now := time.Now()
	entry := cache.entries[key]
	if entry == nil {
		if len(cache.entries) >= displayStatsMaxEntries {
			var oldestKey string
			var oldest *displayStatsEntry
			for k, candidate := range cache.entries {
				if candidate.done == nil && (oldest == nil || candidate.updatedAt.Before(oldest.updatedAt)) {
					oldestKey, oldest = k, candidate
				}
			}
			if oldest == nil {
				cache.mu.Unlock()
				return value, updatedAt, false, errors.New("display statistics refresh capacity reached")
			}
			delete(cache.entries, oldestKey)
		}
		entry = &displayStatsEntry{}
		cache.entries[key] = entry
	}
	if entry.value != nil && now.Sub(entry.updatedAt) < displayStatsTTL {
		value = entry.value.(T)
		updatedAt = entry.updatedAt
		cache.mu.Unlock()
		return value, updatedAt, false, nil
	}
	if entry.done == nil && !now.Before(entry.retryAt) {
		entry.done = make(chan struct{})
		done := entry.done
		// 2026-11-02 coder(lq): One canceled browser request must not cancel a shared refresh; its own timeout still bounds database work.
		go func() {
			loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
			defer cancel()
			result, loadErr := load(loadCtx)
			if loadErr == nil {
				loadErr = loadCtx.Err()
			}
			if loadErr != nil {
				kind, _, _ := strings.Cut(key, ":")
				log.Printf("管理页展示统计刷新失败: kind=%s err=%v", kind, loadErr)
			}
			cache.mu.Lock()
			defer cache.mu.Unlock()
			if loadErr == nil {
				entry.value = result
				entry.updatedAt = time.Now()
				entry.retryAt = time.Time{}
			} else {
				entry.retryAt = time.Now().Add(displayStatsRetryDelay)
			}
			entry.err = loadErr
			entry.done = nil
			close(done)
		}()
	}
	if entry.value != nil && now.Sub(entry.updatedAt) < displayStatsMaxAge {
		value, updatedAt = entry.value.(T), entry.updatedAt
		cache.mu.Unlock()
		return value, updatedAt, true, nil
	}
	done := entry.done
	if done == nil {
		err = entry.err
		cache.mu.Unlock()
		return value, updatedAt, false, err
	}
	cache.mu.Unlock()
	select {
	case <-ctx.Done():
		return value, updatedAt, false, ctx.Err()
	case <-done:
		cache.mu.Lock()
		defer cache.mu.Unlock()
		if entry.err != nil {
			return value, updatedAt, false, entry.err
		}
		return entry.value.(T), entry.updatedAt, false, nil
	}
}

func (cache *displayStatsCache) clear() {
	cache.mu.Lock()
	cache.entries = nil
	cache.mu.Unlock()
}
