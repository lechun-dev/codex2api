package database

// 真 MySQL 5.6 上的「账号智能检查」生命周期测试。
//
// 这份功能在 MySQL 上不是照搬上游 SQL：5.6 没有 JSON 函数 / 窗口函数 / 部分索引 /
// RETURNING，所以设置、入队、认领、收尾、筛选、看板、保留策略都走了分叉实现
// （见 intelligence_check_mysql.go）。这里用独立的临时库把它们从头跑一遍，
// 由 CI 的 mysql56-compatibility job 执行（测试名匹配 TestMySQL.*Integration）。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	ic "github.com/codex2api/internal/intelligencecheck"
)

func mysqlIntelligenceDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("CODEX2API_MYSQL_TEST_DSN"))
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("CODEX2API_TEST_MYSQL_DSN"))
	}
	if dsn == "" {
		t.Skip("set CODEX2API_MYSQL_TEST_DSN to run MySQL intelligence check integration test")
	}
	return dsn
}

var mysqlDSNDatabasePattern = regexp.MustCompile(`^(.*@tcp\([^)]*\)/)([^?]*)(\?.*)?$`)

// mysqlTempDatabase 为一次测试建独立库，避免和同实例其它用例互相污染。
// 受限账号（例如只有单库权限的测试实例）建库失败时退回共用库，由调用方按
// 账号 id 自清理。
func mysqlTempDatabase(t *testing.T, dsn string) string {
	t.Helper()
	name := fmt.Sprintf("c2a_ic_%d", time.Now().UnixNano())
	admin, err := sql.Open("mysql", mysqlDSNWithDatabase(dsn, ""))
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()
	if _, err = admin.ExecContext(context.Background(), "CREATE DATABASE "+name+" CHARACTER SET utf8"); err != nil {
		t.Logf("cannot create temporary database (%v); falling back to the shared schema", err)
		return dsn
	}
	t.Cleanup(func() {
		cleanup, ce := sql.Open("mysql", mysqlDSNWithDatabase(dsn, ""))
		if ce != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name)
	})
	return mysqlDSNWithDatabase(dsn, name)
}

func mysqlDSNWithDatabase(dsn, name string) string {
	if m := mysqlDSNDatabasePattern.FindStringSubmatch(dsn); m != nil {
		return m[1] + name + m[3]
	}
	return dsn
}

func TestMySQLIntelligenceCheckIntegration(t *testing.T) {
	dsn := mysqlTempDatabase(t, mysqlIntelligenceDSN(t))
	ctx := context.Background()
	db, err := New("mysql", dsn)
	if err != nil {
		t.Fatalf("New(mysql) failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	settings, err := db.GetIntelligenceCheckSettings(ctx)
	if err != nil {
		t.Fatalf("GetIntelligenceCheckSettings: %v", err)
	}
	if len(settings.Questions) == 0 {
		t.Fatal("default settings should ship at least one question")
	}
	settings.Enabled = true
	settings.NewAccountsEnabled = true
	settings.Concurrency = 1
	settings.Retention = 2
	settings, err = db.SaveIntelligenceCheckSettings(ctx, settings)
	if err != nil {
		t.Fatalf("SaveIntelligenceCheckSettings: %v", err)
	}

	var createdAccount int64
	t.Cleanup(func() {
		if createdAccount == 0 {
			return
		}
		// 建库失败退回共用库时，把本次用例造的数据清掉。
		_, _ = db.conn.ExecContext(context.Background(), `DELETE FROM intelligence_check_runs WHERE account_id=?`, createdAccount)
		_, _ = db.conn.ExecContext(context.Background(), `DELETE FROM intelligence_check_accounts WHERE account_id=?`, createdAccount)
		_, _ = db.conn.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=?`, createdAccount)
	})
	id, err := db.InsertAccount(ctx, "mysql-ic-account", "local-fixture", "")
	if err != nil {
		t.Fatalf("InsertAccount: %v", err)
	}
	createdAccount = id
	// MySQL 侧没有触发器：调度前会把新账号补进 intelligence_check_accounts。
	if _, err = db.EnqueueDueIntelligenceChecks(ctx, now, 10); err != nil {
		t.Fatalf("EnqueueDueIntelligenceChecks (enrollment): %v", err)
	}
	accounts, total, err := db.ListIntelligenceCheckAccounts(ctx, IntelligenceCheckAccountFilter{AccountID: id})
	if err != nil || total != 1 || len(accounts) != 1 {
		t.Fatalf("ListIntelligenceCheckAccounts: total=%d accounts=%d err=%v", total, len(accounts), err)
	}
	if accounts[0].AccountID != id || accounts[0].Channel != "codex" {
		t.Fatalf("unexpected account projection: %+v", accounts[0])
	}
	// 渠道过滤依赖 MySQL 的 credentials 正则分支。
	filtered, _, err := db.ListIntelligenceCheckAccounts(ctx, IntelligenceCheckAccountFilter{Channel: "claude"})
	if err != nil {
		t.Fatalf("ListIntelligenceCheckAccounts(channel): %v", err)
	}
	if len(filtered) != 0 {
		t.Fatalf("codex account must not match claude filter: %+v", filtered)
	}

	// 手动入队：走 LastInsertId 分支（MySQL 没有 RETURNING）。
	ids, err := db.EnqueueIntelligenceChecks(ctx, []int64{id}, now)
	if err != nil || len(ids) != 1 || ids[0] == 0 {
		t.Fatalf("EnqueueIntelligenceChecks: ids=%v err=%v", ids, err)
	}
	run, err := db.ClaimIntelligenceCheck(ctx, "worker", now, time.Minute)
	if err != nil || run == nil {
		t.Fatalf("ClaimIntelligenceCheck: run=%v err=%v", run, err)
	}
	if run.AccountID != id {
		t.Fatalf("claimed wrong account: %+v", run)
	}
	// 判定列 question_name / duration_ms / source / account_name / channel 都在
	// 收尾时落库，历史筛选依赖它们。
	ok, err := db.FinishIntelligenceCheck(ctx, run.ID, "worker",
		ic.Observation{Complete: true, Output: run.Question.ExpectedAnswer, DurationMS: 7}, now.Add(2*time.Second))
	if err != nil || !ok {
		t.Fatalf("FinishIntelligenceCheck: ok=%v err=%v", ok, err)
	}

	runs, runTotal, err := db.ListIntelligenceCheckRuns(ctx, IntelligenceCheckRunFilter{AccountID: id, Search: "mysql-ic", Channel: "codex"})
	if err != nil {
		t.Fatalf("ListIntelligenceCheckRuns: %v", err)
	}
	if runTotal != 1 || len(runs) != 1 || runs[0].Source != "manual" {
		t.Fatalf("runs=%+v total=%d", runs, runTotal)
	}
	if runs[0].AccountName != "mysql-ic-account" || runs[0].Channel != "codex" {
		t.Fatalf("denormalized columns not populated: %+v", runs[0])
	}
	// 搜索走 account_name 列，大小写不敏感（utf8_general_ci）。
	if _, _, err = db.ListIntelligenceCheckRuns(ctx, IntelligenceCheckRunFilter{Search: "MYSQL-IC"}); err != nil {
		t.Fatalf("ListIntelligenceCheckRuns(search): %v", err)
	}

	bars, err := db.IntelligenceCheckBars(ctx, []int64{id})
	if err != nil {
		t.Fatalf("IntelligenceCheckBars: %v", err)
	}
	if bar, ok := bars[id]; !ok || len(bar.Records) != 1 || bar.Records[0].QuestionName == "" {
		t.Fatalf("bars: %+v", bars)
	}

	// 保留策略：MySQL 5.6 不支持「IN 子查询带 LIMIT」，走先查后删的分支。
	for i := 0; i < 3; i++ {
		at := now.Add(time.Duration(i+1) * time.Minute)
		if _, err = db.EnqueueIntelligenceChecks(ctx, []int64{id}, at); err != nil {
			t.Fatalf("enqueue #%d: %v", i, err)
		}
		claimed, claimErr := db.ClaimIntelligenceCheck(ctx, "worker", at, time.Minute)
		if claimErr != nil || claimed == nil {
			t.Fatalf("claim #%d: %v %v", i, claimed, claimErr)
		}
		if _, err = db.FinishIntelligenceCheck(ctx, claimed.ID, "worker",
			ic.Observation{Complete: true, Output: claimed.Question.ExpectedAnswer, DurationMS: 3}, at.Add(time.Second)); err != nil {
			t.Fatalf("finish #%d: %v", i, err)
		}
	}
	var retained int
	if err = db.conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM intelligence_check_runs WHERE account_id=? AND status NOT IN ('queued','running')`, id).Scan(&retained); err != nil {
		t.Fatalf("count retained runs: %v", err)
	}
	if retained > settings.Retention {
		t.Fatalf("retention kept %d rows, want <= %d", retained, settings.Retention)
	}

	// 状态归属列 state_action 随 state_json 同步，MySQL 的「换组冲突」判断依赖它。
	var action string
	if err = db.conn.QueryRowContext(ctx,
		`SELECT COALESCE(state_action,'') FROM intelligence_check_accounts WHERE account_id=?`, id).Scan(&action); err != nil {
		t.Fatalf("read state_action: %v", err)
	}
	if strings.TrimSpace(action) != "" {
		t.Fatalf("state_action should be empty for a passing account, got %q", action)
	}
}
