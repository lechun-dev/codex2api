package database

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// 2026-11-02 coder(lq): Verify constant-key probes keep the original non-499 semantics, including batch boundaries and out-of-order inserts.
func TestMySQLLastUsedConstantProbes(t *testing.T) {
	db := newMySQLPromptReviewIntegrationDB(t)
	ctx := context.Background()
	var ids []int64
	want := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for i := 0; i < 35; i++ {
		id, err := db.InsertAPIKey(ctx, fmt.Sprintf("last-used-%d", i), fmt.Sprintf("sk-last-used-test-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		for _, row := range []struct {
			status int
			at     time.Time
		}{{500, want}, {200, want.Add(-time.Hour)}, {499, want.Add(time.Hour)}} {
			if i == 1 && row.status != 499 || i == 2 {
				continue
			}
			if _, err := db.conn.ExecContext(ctx, "INSERT INTO usage_logs (api_key_id, status_code, created_at) VALUES ($1,$2,$3)", id, row.status, db.timeArg(row.at)); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := db.ListAPIKeyLastUsedAt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 33 {
		t.Fatalf("got %d keys, want 33", len(got))
	}
	for i, id := range ids {
		if i == 1 || i == 2 {
			if _, ok := got[id]; ok {
				t.Fatalf("key %d must be absent", id)
			}
		} else if !got[id].Equal(want) {
			t.Fatalf("key %d: %v, want %v", id, got[id], want)
		}
	}
	query, _ := apiKeyLastUsedMySQLBatch(ids[:2])
	assertNoMySQL56IncompatibleSQL(t, rewriteSQLForMySQL(query))
}

// 2026-11-02 coder(lq): A reproducible local-only comparison; never use a production DSN for benchmarks.
func TestMySQLLastUsedProbePerformance(t *testing.T) {
	if os.Getenv("CODEX2API_MYSQL_PERF_TEST") != "1" {
		t.Skip("set CODEX2API_MYSQL_PERF_TEST=1 for the isolated 262k-row comparison")
	}
	db := newMySQLPromptReviewIntegrationDB(t)
	ctx := context.Background()
	// The existing index is sufficient; no schema/index change is part of the fix.
	if _, err := db.conn.ExecContext(ctx, "CREATE INDEX idx_usage_logs_api_created_status ON usage_logs(api_key_id,created_at,status_code)"); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < 64; i++ {
		id, err := db.InsertAPIKey(ctx, fmt.Sprintf("perf-%d", i), fmt.Sprintf("sk-perf-last-used-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// About 262k rows, including cancellations at the newest end of each key.
	for _, id := range ids {
		var values []string
		for i := 0; i < 4096; i++ {
			status := 200
			if i >= 4090 {
				status = 499
			}
			values = append(values, fmt.Sprintf("(%d,%d,DATE_ADD('2026-10-01 00:00:00', INTERVAL %d SECOND))", id, status, i))
		}
		if _, err := db.conn.ExecContext(ctx, "INSERT INTO usage_logs(api_key_id,status_code,created_at) VALUES "+strings.Join(values, ",")); err != nil {
			t.Fatal(err)
		}
	}
	old := func() (map[int64]time.Time, error) {
		rows, err := db.conn.QueryContext(ctx, `SELECT k.id,(SELECT u.created_at FROM usage_logs u WHERE u.api_key_id=k.id AND u.status_code<>499 ORDER BY u.created_at DESC LIMIT 1) FROM api_keys k WHERE k.id>0`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		result := make(map[int64]time.Time)
		for rows.Next() {
			var id int64
			var raw interface{}
			if err := rows.Scan(&id, &raw); err != nil {
				return nil, err
			}
			at, err := parseDBTimeValue(raw)
			if err != nil {
				return nil, err
			}
			result[id] = at
		}
		return result, rows.Err()
	}
	for i := 0; i < 3; i++ {
		start := time.Now()
		expected, err := old()
		before := time.Since(start)
		if err != nil || len(expected) != len(ids) {
			t.Fatalf("old: n=%d err=%v", len(expected), err)
		}
		start = time.Now()
		got, err := db.ListAPIKeyLastUsedAt(ctx)
		after := time.Since(start)
		if err != nil || len(got) != len(ids) {
			t.Fatalf("new: n=%d err=%v", len(got), err)
		}
		for id, at := range expected {
			if !got[id].Equal(at) {
				t.Fatalf("mismatched key %d: before=%v after=%v", id, at, got[id])
			}
		}
		t.Logf("same warm dataset: correlated=%s constant-probes=%s rows=%d", before, after, 64*4096)
	}
}
