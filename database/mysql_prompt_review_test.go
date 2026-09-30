package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

var promptReviewColumnsForTest = []mysqlColumnDefinition{
	{"prompt_filter_logs", "reviewed", "TINYINT(1) DEFAULT 0"},
	{"prompt_filter_logs", "review_confidence", "DOUBLE NULL"},
	{"prompt_filter_logs", "review_threshold", "DOUBLE NULL"},
	{"prompt_filter_logs", "review_reason", "TEXT NULL"},
	{"prompt_filter_logs", "review_endpoint", "VARCHAR(512) DEFAULT ''"},
	{"prompt_filter_logs", "review_request_mode", "VARCHAR(32) DEFAULT ''"},
	{"prompt_filter_logs", "review_latency_ms", "BIGINT NULL"},
}

func TestMySQLPromptReviewFreshSchema(t *testing.T) {
	ddl := promptFilterLogsMySQLDDL()
	for _, column := range promptReviewColumnsForTest {
		if !strings.Contains(ddl, column.name+" "+column.def) {
			t.Errorf("fresh MySQL schema missing %s %s", column.name, column.def)
		}
	}
	assertNoMySQL56IncompatibleSQL(t, ddl)
}

func TestMySQLPromptReviewStartupUpgrade(t *testing.T) {
	// Missing schema metadata, but data migrations have already been applied.
	capture := &mysqlCaptureDriver{queryRow: []driver.Value{int64(0)}, execRowsAffectedSet: true}
	db := newMySQLCaptureDB(t, capture)
	if err := db.migrateMySQL(context.Background()); err != nil {
		t.Fatal(err)
	}
	queries := strings.Join(capture.queries, "\n")
	for _, column := range promptReviewColumnsForTest {
		want := fmt.Sprintf("ALTER TABLE `prompt_filter_logs` ADD COLUMN `%s` %s", column.name, column.def)
		if !strings.Contains(queries, want) {
			t.Errorf("startup upgrade missing %s", want)
		}
	}
}

func TestMySQLPromptReviewManualScripts(t *testing.T) {
	for _, name := range []string{"mysql56_prompt_review_fields.sql", "mysql56_v3.0.5.sql"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "docs", "sql", name))
			if err != nil {
				t.Fatal(err)
			}
			for _, column := range promptReviewColumnsForTest {
				if !strings.Contains(string(raw), "'"+column.name+"'") || !strings.Contains(string(raw), strings.ReplaceAll(column.def, "'", "''")) {
					t.Errorf("manual script missing %s %s", column.name, column.def)
				}
			}
		})
	}
}

// This test creates and drops its own disposable database. The supplied test
// account needs CREATE/DROP DATABASE; no existing application tables are altered.
func newMySQLPromptReviewIntegrationDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("CODEX2API_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("requires an isolated MySQL server via CODEX2API_MYSQL_TEST_DSN")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid MySQL test DSN")
	}
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("c2a_prompt_review_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP DATABASE `" + name + "`"); err != nil {
			t.Errorf("cleanup disposable database: %v", err)
		}
	})
	cfg.DBName = name
	db, err := New("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMySQLPromptReviewIntegration(t *testing.T) {
	db := newMySQLPromptReviewIntegrationDB(t)
	ctx := context.Background()

	t.Run("fresh-page-query", func(t *testing.T) {
		if _, _, err := db.ListPromptFilterLogsPage(ctx, PromptFilterLogQuery{}); err != nil {
			t.Fatalf("Prompt page query after fresh startup: %v", err)
		}
	})
	if t.Failed() {
		return
	}

	for _, repair := range []string{"startup", "manual-script"} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%v", repair, partial), func(t *testing.T) {
				// Retain reviewed and its populated value in the partial-upgrade case.
				var drops []string
				for _, column := range promptReviewColumnsForTest {
					if !partial || column.name != "reviewed" {
						drops = append(drops, "DROP COLUMN `"+column.name+"`")
					}
				}
				if _, err := db.conn.ExecContext(ctx, "ALTER TABLE prompt_filter_logs "+strings.Join(drops, ", ")); err != nil {
					t.Fatal(err)
				}
				seed := "INSERT INTO prompt_filter_logs (source, text_preview) VALUES ('review-regression', 'preserved')"
				if partial {
					seed = "INSERT INTO prompt_filter_logs (source, text_preview, reviewed) VALUES ('review-regression', 'preserved', 1)"
				}
				if _, err := db.conn.ExecContext(ctx, seed); err != nil {
					t.Fatal(err)
				}
				// Both paths must repair a legacy schema and be safe to repeat.
				for i := 0; i < 2; i++ {
					if repair == "startup" {
						if err := db.migrateMySQL(ctx); err != nil {
							t.Fatal(err)
						}
					} else {
						runPromptReviewRepairScript(t, db)
					}
				}
				logs, total, err := db.ListPromptFilterLogsPage(ctx, PromptFilterLogQuery{Source: "review-regression"})
				if err != nil || total != 1 || len(logs) != 1 {
					t.Fatalf("page after repair: total=%d rows=%d err=%v", total, len(logs), err)
				}
				log := logs[0]
				if log.TextPreview != "preserved" || log.Reviewed != partial || log.ReviewConfidence != nil || log.ReviewThreshold != nil || log.ReviewLatencyMS != nil || log.ReviewReason != "" || log.ReviewEndpoint != "" || log.ReviewRequestMode != "" {
					t.Fatalf("legacy row/defaults changed: %+v", log)
				}
				confidence, threshold, latency := 0.9, 0.8, int64(123)
				if err := db.InsertPromptFilterLog(ctx, &PromptFilterLogInput{
					Source: "review-regression", Action: "block", MatchedPatterns: "[]",
					Reviewed: true, ReviewConfidence: &confidence, ReviewThreshold: &threshold,
					ReviewReason: "review regression", ReviewEndpoint: "/responses",
					ReviewRequestMode: "chat", ReviewLatencyMS: &latency,
				}); err != nil {
					t.Fatalf("insert reviewed log: %v", err)
				}
				logs, _, err = db.ListPromptFilterLogsPage(ctx, PromptFilterLogQuery{Source: "review-regression", ReviewState: "reviewed"})
				if err != nil || len(logs) == 0 {
					t.Fatalf("reviewed filter: rows=%d err=%v", len(logs), err)
				}
				log = logs[0]
				if !log.Reviewed || log.ReviewConfidence == nil || *log.ReviewConfidence != confidence || log.ReviewThreshold == nil || *log.ReviewThreshold != threshold || log.ReviewLatencyMS == nil || *log.ReviewLatencyMS != latency || log.ReviewReason != "review regression" || log.ReviewEndpoint != "/responses" || log.ReviewRequestMode != "chat" {
					t.Fatalf("review fields failed to round-trip: %+v", log)
				}
				if _, err := db.conn.ExecContext(ctx, "DELETE FROM prompt_filter_logs"); err != nil {
					t.Fatal(err)
				}
			})
			if t.Failed() {
				return
			}
		}
	}
}

func runPromptReviewRepairScript(t *testing.T, db *DB) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "docs", "sql", "mysql56_prompt_review_fields.sql"))
	if err != nil {
		t.Fatal(err)
	}
	// This script has only full-line comments and no semicolons inside literals.
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			lines = append(lines, line)
		}
	}
	conn, err := db.conn.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, statement := range strings.Split(strings.Join(lines, "\n"), ";") {
		if statement = strings.TrimSpace(statement); statement != "" {
			if _, err := conn.ExecContext(context.Background(), statement); err != nil {
				t.Fatalf("manual repair script: %v", err)
			}
		}
	}
}
