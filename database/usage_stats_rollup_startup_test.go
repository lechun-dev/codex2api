package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type usageStatsRollupContextKey struct{}

func TestUsageStatsRollupStartupContextKeepsValuesButNotSchemaCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), usageStatsRollupContextKey{}, "startup"))
	cancelParent()

	ctx, cancel := usageStatsRollupStartupContext(parent)
	defer cancel()

	if got := ctx.Value(usageStatsRollupContextKey{}); got != "startup" {
		t.Fatalf("startup context value = %v, want startup", got)
	}
	select {
	case <-ctx.Done():
		t.Fatalf("rollup context inherited the canceled schema deadline: %v", ctx.Err())
	default:
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("rollup context has no bounded deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 4*time.Minute || remaining > usageStatsRollupInitTimeout {
		t.Fatalf("rollup context remaining deadline = %v, want (4m, %v]", remaining, usageStatsRollupInitTimeout)
	}
}

func TestEnsureUsageStatsRollupUsesMySQL56CompatibleStatements(t *testing.T) {
	capture := &mysqlCaptureDriver{
		queryRows: [][]driver.Value{
			{int64(1)},
			{int64(1), int64(2)},
		},
	}
	driverName := fmt.Sprintf("codex2api-mysql-rollup-%d", atomic.AddUint64(&mysqlCaptureDriverSequence, 1))
	sql.Register(driverName, mysqlRewriteDriver{inner: capture})

	conn, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	db := &DB{conn: conn, driver: "mysql"}
	if err := db.ensureUsageStatsRollup(context.Background()); err != nil {
		t.Fatalf("ensureUsageStatsRollup() error = %v", err)
	}
	if capture.execCount != 2 {
		t.Fatalf("rollup schema statement count = %d, want 2 independent statements", capture.execCount)
	}
	if len(capture.queries) != 4 {
		t.Fatalf("captured query count = %d, want 2 DDL statements and 2 MySQL schema/state queries", len(capture.queries))
	}
	for _, query := range capture.queries {
		if strings.Count(strings.ToUpper(query), "CREATE TABLE") > 1 {
			t.Fatalf("multiple CREATE TABLE statements were sent in one MySQL call: %s", query)
		}
		assertNoMySQL56IncompatibleSQL(t, query)
	}
}
