package database

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMySQLScheduledConnectionTestConfigSQL(t *testing.T) {
	plan := `{"enabled":true,"model":"gpt-5.5","content":"hi","weekday_times":["12:00"]}`
	for _, tc := range []struct {
		name, credentials string
		want              bool
	}{
		{"codex-string", `{"upstream_type":"codex","scheduled_connection_tests":` + scheduledTestJSONString(plan) + `,"access_token":"not-to-be-returned"}`, true},
		{"responses-object", `{"upstream_type":"openai_responses","scheduled_connection_tests":` + plan + `}`, true},
		{"legacy", `{"scheduled_connection_tests":` + scheduledTestJSONString(plan) + `}`, true},
		{"upper-case", `{"upstream_type":"CODEX","scheduled_connection_tests":` + scheduledTestJSONString(plan) + `}`, true},
		{"other-upstream", `{"upstream_type":"grok","scheduled_connection_tests":` + plan + `}`, false},
		{"missing", `{"upstream_type":"codex"}`, false},
		{"null", `{"scheduled_connection_tests":null}`, false},
		{"empty", `{"scheduled_connection_tests":""}`, false},
		{"malformed", `{"scheduled_connection_tests":`, false},
		{"nested-decoy", `{"nested":{"scheduled_connection_tests":` + plan + `}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &mysqlCaptureDriver{queryRow: []driver.Value{int64(7), tc.credentials}}
			db := newMySQLCaptureDB(t, capture)
			rows, err := db.ListScheduledConnectionTestConfigs(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			assertNoMySQL56IncompatibleSQL(t, capture.query)
			if tc.want {
				if len(rows) != 1 || rows[0].AccountID != 7 || rows[0].Config != plan {
					t.Fatalf("unexpected projected config: %+v", rows)
				}
			} else if len(rows) != 0 {
				t.Fatalf("unexpected plans: %+v", rows)
			}
			for _, row := range rows {
				if strings.Contains(row.Config, "not-to-be-returned") {
					t.Fatal("credential leaked into plan")
				}
			}
		})
	}
}

func scheduledTestJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestMySQLScheduledConnectionTestsIntegration(t *testing.T) {
	db := newMySQLPromptReviewIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	plan := `{"enabled":true,"model":"gpt-5.5","content":"hi","weekday_times":["12:00"]}`
	var wantIDs []int64
	for _, tc := range []struct {
		name, upstream, status string
		enabled, want          bool
	}{
		{"codex", "codex", "active", true, true},
		{"responses", "openai_responses", "active", true, true},
		{"legacy", "", "active", true, true},
		{"disabled", "codex", "active", false, false},
		{"deleted", "codex", "deleted", true, false},
		{"grok", "grok", "active", true, false},
	} {
		id, err := db.InsertAccountWithUpstream(ctx, tc.name, "openai", "oauth", map[string]interface{}{"upstream_type": tc.upstream, "scheduled_connection_tests": plan}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.conn.ExecContext(ctx, `UPDATE accounts SET enabled=$1,status=$2 WHERE id=$3`, tc.enabled, tc.status, id); err != nil {
			t.Fatal(err)
		}
		if tc.want {
			wantIDs = append(wantIDs, id)
		}
	}
	rows, err := db.ListScheduledConnectionTestConfigs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(wantIDs) {
		t.Fatalf("plans=%d want=%d", len(rows), len(wantIDs))
	}
	for i, row := range rows {
		if row.AccountID != wantIDs[i] || row.Config != plan {
			t.Fatalf("unexpected config: %+v", row)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	key := "scheduled_connection_test:2026-11-03T12:00"
	var claimed atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			ok, err := db.TryClaimMaintenanceJob(ctx, wantIDs[0], key, now)
			if err != nil {
				errs <- err
			}
			if ok {
				claimed.Add(1)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if claimed.Load() != 1 {
		t.Fatalf("claimed %d times, want 1", claimed.Load())
	}
	summary := `{"status":"success","success":true}`
	if err := db.FinishMaintenanceJob(ctx, wantIDs[0], key, summary); err != nil {
		t.Fatal(err)
	}
	// A completed slot must not be reset or claimed again; another slot is independent.
	if ok, err := db.TryClaimMaintenanceJob(ctx, wantIDs[0], key, now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("reclaimed completed slot: %v %v", ok, err)
	}
	if ok, err := db.TryClaimMaintenanceJob(ctx, wantIDs[0], key+"-next", now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("next slot: %v %v", ok, err)
	}
	if ok, err := db.TryClaimMaintenanceJob(ctx, wantIDs[0], "other-kind", now.Add(time.Hour)); err != nil || !ok {
		t.Fatalf("other kind: %v %v", ok, err)
	}
	jobs, err := db.ListOneShotMaintenanceJobs(ctx, wantIDs[0], "scheduled_connection_test:", 10)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("history: %+v %v", jobs, err)
	}
	if jobs[0].JobKind != key+"-next" || jobs[1].LastError != summary || jobs[1].Attempts != 1 || !jobs[1].DueAt.Equal(now) {
		t.Fatalf("history corrupted: %+v", jobs)
	}
	limited, err := db.ListOneShotMaintenanceJobs(ctx, wantIDs[0], "scheduled_connection_test:", 1)
	if err != nil || len(limited) != 1 || limited[0].JobKind != key+"-next" {
		t.Fatalf("history limit: %+v %v", limited, err)
	}
}
