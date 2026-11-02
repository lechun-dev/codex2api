package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

func maintenanceRowForTest(id int64, now time.Time) []driver.Value {
	return []driver.Value{id, MaintenanceJobGrokFreshness, now.Add(-time.Minute), "", nil, int64(0), ""}
}

// 2026-11-02 coder(lq): A deadlock must rollback and rediscover; only the final committed attempt may return jobs.
func TestMySQLMaintenanceClaimRetriesWithoutLeakingRolledBackJobs(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	capture := &mysqlCaptureDriver{
		queryRows:    [][]driver.Value{{int64(7)}, maintenanceRowForTest(7, now), {int64(8)}, maintenanceRowForTest(8, now)},
		commitErrors: []error{&mysql.MySQLError{Number: 1213}},
	}
	db := newMySQLCaptureDB(t, capture)
	jobs, err := db.ClaimMaintenanceJobs(context.Background(), MaintenanceJobGrokFreshness, "worker", now, time.Minute, 10)
	if err != nil || len(jobs) != 1 || jobs[0].EntityID != 8 || jobs[0].Attempts != 1 {
		t.Fatalf("jobs=%+v err=%v, want only committed job 8", jobs, err)
	}
	if capture.commits != 2 {
		t.Fatalf("commits=%d", capture.commits)
	}
	for _, query := range capture.queries {
		assertNoMySQL56IncompatibleSQL(t, query)
		if strings.Contains(query, "FOR UPDATE") && (!strings.Contains(query, "WHERE entity_id=") || strings.Contains(query, "ORDER BY due_at")) {
			t.Fatalf("locking a secondary range: %s", query)
		}
	}
}

func TestMySQLMaintenanceClaimFailureReturnsNoJobs(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	capture := &mysqlCaptureDriver{
		queryRows:  [][]driver.Value{{int64(7)}, maintenanceRowForTest(7, now)},
		execErrors: map[int]error{1: errors.New("connection failed")},
	}
	db := newMySQLCaptureDB(t, capture)
	jobs, err := db.ClaimMaintenanceJobs(context.Background(), MaintenanceJobGrokFreshness, "worker", now, time.Minute, 10)
	if err == nil || len(jobs) != 0 || capture.rollbacks != 1 || capture.execCount != 1 {
		t.Fatalf("jobs=%+v err=%v rollbacks=%d writes=%d", jobs, err, capture.rollbacks, capture.execCount)
	}
}

func TestMySQLMaintenanceClaimRechecksDiscoveredCandidates(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, changed := range []string{"due", "leased", "deleted"} {
		t.Run(changed, func(t *testing.T) {
			row := maintenanceRowForTest(7, now)
			switch changed {
			case "due":
				row[2] = now.Add(time.Hour)
			case "leased":
				row[3], row[4] = "other-worker", now.Add(time.Hour)
			case "deleted":
				row = nil
			}
			capture := &mysqlCaptureDriver{queryRows: [][]driver.Value{{int64(7)}, row}}
			db := newMySQLCaptureDB(t, capture)
			jobs, err := db.ClaimMaintenanceJobs(context.Background(), MaintenanceJobGrokFreshness, "worker", now, time.Minute, 10)
			if err != nil || len(jobs) != 0 || capture.execCount != 0 {
				t.Fatalf("jobs=%+v err=%v writes=%d", jobs, err, capture.execCount)
			}
		})
	}
}

func TestMySQLMaintenanceRetryIsBoundedAndSelective(t *testing.T) {
	for _, number := range []uint16{1213, 1205, 2006} {
		db := &DB{driver: "mysql"}
		calls := 0
		err := db.retryMaintenanceWrite(context.Background(), "test", func() error {
			calls++
			return fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: number})
		})
		want := 1
		if number == 1213 {
			want = 4
		}
		if err == nil || calls != want {
			t.Fatalf("number=%d calls=%d err=%v", number, calls, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	db := &DB{driver: "mysql"}
	calls := 0
	err := db.retryMaintenanceWrite(ctx, "test", func() error {
		calls++
		cancel()
		return &mysql.MySQLError{Number: 1213}
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancellation: calls=%d err=%v", calls, err)
	}
}

func TestMySQLMaintenanceCompletionAndFailureRetry(t *testing.T) {
	for _, op := range []string{"complete", "fail"} {
		t.Run(op, func(t *testing.T) {
			capture := &mysqlCaptureDriver{execErrors: map[int]error{1: &mysql.MySQLError{Number: 1213}}}
			db := newMySQLCaptureDB(t, capture)
			var err error
			if op == "complete" {
				err = db.CompleteMaintenanceJob(context.Background(), 7, MaintenanceJobGrokFreshness, "worker", time.Now().Add(time.Hour))
			} else {
				err = db.FailMaintenanceJob(context.Background(), 7, MaintenanceJobGrokFreshness, "worker", time.Now().Add(time.Hour), errors.New("probe failed"))
			}
			if err != nil || capture.execCount != 2 {
				t.Fatalf("writes=%d err=%v", capture.execCount, err)
			}
		})
	}
}

// 2026-11-02 coder(lq): Real concurrent claim/complete operations must not duplicate leases; expired owners must not complete replacement leases.
func TestMySQLMaintenanceConcurrentClaims(t *testing.T) {
	db := newMySQLPromptReviewIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Second)
	for i := 1; i <= 40; i++ {
		if _, err := db.conn.ExecContext(ctx, `INSERT INTO maintenance_jobs(entity_id,job_kind,due_at) VALUES ($1,$2,$3)`, i, MaintenanceJobGrokFreshness, db.timeArg(now.Add(-time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	claimed := make(map[int64]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		wg.Go(func() {
			owner := fmt.Sprintf("worker-%d", worker)
			for {
				jobs, err := db.ClaimMaintenanceJobs(ctx, MaintenanceJobGrokFreshness, owner, now, time.Minute, 4)
				if err != nil {
					errs <- err
					return
				}
				if len(jobs) == 0 {
					return
				}
				for _, job := range jobs {
					mu.Lock()
					duplicate := claimed[job.EntityID]
					claimed[job.EntityID] = true
					mu.Unlock()
					if duplicate {
						errs <- fmt.Errorf("duplicate lease: %d", job.EntityID)
						return
					}
					if err := db.CompleteMaintenanceJob(ctx, job.EntityID, job.JobKind, owner, now.Add(time.Hour)); err != nil {
						errs <- err
						return
					}
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(claimed) != 40 {
		t.Fatalf("claimed=%d, want 40", len(claimed))
	}
	// Re-lease an expired job; the previous owner cannot mutate it.
	if _, err := db.conn.ExecContext(ctx, `UPDATE maintenance_jobs SET due_at=$1,lease_owner='expired',lease_until=$1 WHERE entity_id=1`, db.timeArg(now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	jobs, err := db.ClaimMaintenanceJobs(ctx, MaintenanceJobGrokFreshness, "replacement", now, time.Minute, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("reclaim: jobs=%+v err=%v", jobs, err)
	}
	if err := db.CompleteMaintenanceJob(ctx, 1, MaintenanceJobGrokFreshness, "expired", now.Add(time.Hour)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired owner completed replacement: %v", err)
	}
}
