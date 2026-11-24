package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteImageJobNullableErrorMessage(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "nullable-image-job.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	testImageJobNullableErrorMessage(t, db)
}

func TestMySQLImageJobNullableErrorMessageIntegration(t *testing.T) {
	dsn := os.Getenv("CODEX2API_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set CODEX2API_MYSQL_TEST_DSN to run MySQL integration test")
	}
	db, err := New("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	testImageJobNullableErrorMessage(t, db)
}

// 2026-11-24 coder(lq): Exercise the real SQL scanners with MySQL-compatible NULLs across history and polling readers.
func testImageJobNullableErrorMessage(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	const keyID int64 = 923456789
	id, err := db.InsertImageGenerationJob(ctx, ImageGenerationJobInput{
		Prompt: "nullable error regression", APIKeyID: keyID,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.DeleteImageGenerationJob(ctx, id) })

	var initialError sql.NullString
	if err := db.conn.QueryRowContext(ctx, `SELECT error_message FROM image_generation_jobs WHERE id=$1`, id).Scan(&initialError); err != nil {
		t.Fatal(err)
	}
	if !initialError.Valid || initialError.String != "" {
		t.Errorf("new job error_message = %+v, want non-NULL empty string", initialError)
	}

	for _, tc := range []struct {
		name, status string
		stored       any
		wantError    string
		wantWarning  string
	}{
		{"queued_null", ImageJobQueued, nil, "", ""},
		{"running_null", ImageJobRunning, nil, "", ""},
		{"succeeded_null", ImageJobSucceeded, nil, "", ""},
		{"failed_null", ImageJobFailed, nil, "", ""},
		{"empty", ImageJobQueued, "", "", ""},
		{"failed_message", ImageJobFailed, "upstream rejected", "upstream rejected", ""},
		{"success_warning", ImageJobSucceeded, "partial output", "", "partial output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.conn.ExecContext(ctx, `UPDATE image_generation_jobs SET status=$1,error_message=$2 WHERE id=$3`, tc.status, tc.stored, id); err != nil {
				t.Fatal(err)
			}
			check := func(job *ImageGenerationJob, err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				if job == nil || job.ID != id || job.Status != tc.status || job.ErrorMessage != tc.wantError || job.Warning != tc.wantWarning {
					t.Fatalf("unexpected job: %+v", job)
				}
			}
			check(db.GetImageGenerationJob(ctx, id))
			check(db.GetImageGenerationJobResult(ctx, id, keyID))
			check(db.GetImageJobSummary(ctx, id))
			for _, summaries := range []bool{false, true} {
				var page *ImageJobPage
				var err error
				if summaries {
					page, err = db.ListImageJobSummaries(ctx, 1, 10, keyID)
				} else {
					page, err = db.ListImageGenerationJobs(ctx, 1, 10, keyID)
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Jobs) != 1 {
					t.Fatalf("jobs = %d, want 1", len(page.Jobs))
				}
				check(&page.Jobs[0], nil)
			}
			jobs, err := db.ListImageGenerationJobResults(ctx, []int64{id}, keyID)
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 1 {
				t.Fatalf("results = %d, want 1", len(jobs))
			}
			check(&jobs[0], nil)
			if _, err := db.GetImageGenerationJobResult(ctx, id, keyID+1); err != sql.ErrNoRows {
				t.Fatalf("other key read: %v, want sql.ErrNoRows", err)
			}
		})
	}
}
