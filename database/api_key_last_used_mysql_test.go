package database

import (
	"context"
	"database/sql/driver"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// 2026-11-02 coder(lq): Keep the index-aligned correlated query to one round trip with MySQL 5.6-compatible SQL.
func TestMySQLLastUsedQueryUsesIndexOrder(t *testing.T) {
	want := time.Now().UTC().Truncate(time.Second)
	capture := &mysqlCaptureDriver{queryRows: [][]driver.Value{{int64(7), want}}}
	db := newMySQLCaptureDB(t, capture)
	got, err := db.ListAPIKeyLastUsedAt(context.Background())
	if err != nil || !got[7].Equal(want) {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if len(capture.queries) != 1 {
		t.Fatalf("queries=%d, want one correlated query", len(capture.queries))
	}
	query := capture.queries[0]
	for _, fragment := range []string{"u.api_key_id = k.id", "u.status_code <> 499", "ORDER BY u.api_key_id DESC, u.created_at DESC", "LIMIT 1", "k.id > 0"} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("query missing %q: %s", fragment, query)
		}
	}
	assertNoMySQL56IncompatibleSQL(t, query)
}

// 2026-11-02 coder(lq): Index-aligned sorting preserves latest non-499 timestamps, including failed requests and out-of-order inserts.
func TestMySQLLastUsedIndexOrder(t *testing.T) {
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
}

// 2026-11-02 coder(lq): A reproducible local-only comparison; never use a production DSN for benchmarks.
func TestMySQLLastUsedIndexOrderPerformance(t *testing.T) {
	if os.Getenv("CODEX2API_MYSQL_PERF_TEST") != "1" {
		t.Skip("set CODEX2API_MYSQL_PERF_TEST=1 for the isolated 262k-row comparison")
	}
	db := newMySQLPromptReviewIntegrationDB(t)
	ctx := context.Background()
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
	readQuery := func(query string) (map[int64]time.Time, error) {
		rows, err := db.conn.QueryContext(ctx, query)
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
	originalQuery := `SELECT k.id,(SELECT u.created_at FROM usage_logs u WHERE u.api_key_id=k.id AND u.status_code<>499 ORDER BY u.created_at DESC LIMIT 1) FROM api_keys k WHERE k.id>0`
	orderedQuery := strings.Replace(originalQuery, "ORDER BY u.created_at DESC", "ORDER BY u.api_key_id DESC, u.created_at DESC", 1)
	// 2026-11-02 coder(lq): Measure both the default pair index and the report's existing covering index, without changing production DDL.
	for _, profile := range []string{"pair-index", "covering-index"} {
		t.Run(profile, func(t *testing.T) {
			if profile == "covering-index" {
				if _, err := db.conn.ExecContext(ctx, "CREATE INDEX idx_usage_logs_api_created_status ON usage_logs(api_key_id,created_at,status_code)"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.conn.ExecContext(ctx, "ANALYZE TABLE usage_logs"); err != nil {
				t.Fatal(err)
			}
			for name, query := range map[string]string{"original": originalQuery, "index-ordered": orderedQuery} {
				rows, err := db.conn.QueryContext(ctx, "EXPLAIN "+query)
				if err != nil {
					t.Fatal(err)
				}
				columns, err := rows.Columns()
				if err != nil {
					rows.Close()
					t.Fatal(err)
				}
				for rows.Next() {
					values := make([]interface{}, len(columns))
					for i := range values {
						values[i] = new(interface{})
					}
					if err := rows.Scan(values...); err != nil {
						rows.Close()
						t.Fatal(err)
					}
					var plan []string
					for i, value := range values {
						v := *(value.(*interface{}))
						if b, ok := v.([]byte); ok {
							v = string(b)
						}
						plan = append(plan, fmt.Sprintf("%s=%v", columns[i], v))
						if name == "index-ordered" && columns[i] == "Extra" && strings.Contains(strings.ToLower(fmt.Sprint(v)), "filesort") {
							rows.Close()
							t.Fatalf("index-aligned query still filesorts: %v", v)
						}
					}
					t.Logf("%s plan: %s", name, strings.Join(plan, " "))
				}
				if err := rows.Err(); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				rows.Close()
			}
			for i := 0; i < 3; i++ {
				start := time.Now()
				expected, err := readQuery(originalQuery)
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
				t.Logf("same dataset: original=%s index-ordered=%s rows=%d", before, after, 64*4096)
			}
		})
	}
}
