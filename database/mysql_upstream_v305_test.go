package database

import (
	"context"
	"database/sql/driver"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMySQL305ChannelMonitorSchema(t *testing.T) {
	capture := &mysqlCaptureDriver{queryRow: []driver.Value{int64(0)}}
	db := newMySQLCaptureDB(t, capture)
	if err := db.createChannelMonitorSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	tables := 0
	for _, q := range capture.queries {
		assertNoMySQL56IncompatibleSQL(t, q)
		for _, bad := range []string{"TEXT NOT NULL DEFAULT", "BIGSERIAL", "TIMESTAMPTZ", "checked_at DESC"} {
			if strings.Contains(q, bad) {
				t.Fatalf("incompatible %q: %s", bad, q)
			}
		}
		if strings.HasPrefix(q, "CREATE TABLE") {
			tables++
			if !strings.Contains(q, "FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE") || !strings.Contains(q, "ENGINE=InnoDB DEFAULT CHARSET=utf8") {
				t.Fatalf("missing table options: %s", q)
			}
		}
	}
	if tables != 2 {
		t.Fatalf("tables=%d", tables)
	}
	if got := db.channelMonitorTimestampPlaceholder(7); got != "$7" {
		t.Fatalf("timestamp placeholder: %s", got)
	}
	if got := db.channelMonitorVarcharPlaceholder(1); got != "$1" {
		t.Fatalf("varchar placeholder: %s", got)
	}
}

func TestMySQL305ImageSchema(t *testing.T) {
	capture := &mysqlCaptureDriver{queryRow: []driver.Value{int64(0)}}
	db := newMySQLCaptureDB(t, capture)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := db.InitImageJobQueue(ctx); err != nil {
			t.Fatal(err)
		}
		if err := db.InitImageMaintenance(ctx); err != nil {
			t.Fatal(err)
		}
		if err := db.ensureImageAssetRetentionSchema(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range capture.queries {
		assertNoMySQL56IncompatibleSQL(t, q)
		for _, bad := range []string{"ADD COLUMN IF NOT EXISTS", "TEXT NOT NULL DEFAULT", ") WHERE expires_at"} {
			if strings.Contains(q, bad) {
				t.Fatalf("incompatible %q: %s", bad, q)
			}
		}
	}
	queries := strings.Join(capture.queries, "\n")
	for _, want := range []string{"`queue_owner` VARCHAR(255) NOT NULL DEFAULT ''", "`delete_after_read` TINYINT(1) NOT NULL DEFAULT 0", "INFORMATION_SCHEMA.STATISTICS"} {
		if !strings.Contains(queries, want) {
			t.Fatalf("missing %q: %s", want, queries)
		}
	}
}

func TestMySQL305SettingsAndUsageSQL(t *testing.T) {
	ddl := systemSettingsMySQLDDL()
	for _, name := range []string{"codex_basispoints_enabled", "codex_basispoints_models", "codex_basispoints_403_pause_disabled", "codex_basispoints_403_probe_interval_minutes", "codex_basispoints_429_cooldown_seconds", "codex_basispoints_cache_creation_as_input", "auto_reset_credits_on_exhaustion_enabled", "codex_turn_state_template_cache_enabled", "codex_turn_state_account_mode", "codex_synced_desktop_mac_build", "codex_synced_desktop_windows_build", "codex_synced_vscode_build"} {
		if !strings.Contains(ddl, name+" ") {
			t.Fatalf("missing setting %s", name)
		}
	}
	capture := &mysqlCaptureDriver{}
	db := newMySQLCaptureDB(t, capture)
	if err := db.batchInsertLogsChunk(context.Background(), db.conn, []usageLogEntry{{AccountID: 1, StoreUsageLog: true}}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(capture.query, "?") != 75 || len(capture.args) != 75 {
		t.Fatalf("usage insert columns/args: %d/%d", strings.Count(capture.query, "?"), len(capture.args))
	}
	for _, name := range []string{"request_text", "session_id", "upstream_response_model", "upstream_model_mismatch", "turn_state_overridden", "turn_state_rewrite_note", "injected_turn_state", "upstream_turn_state", "daybreak_program", "video_seconds"} {
		if !strings.Contains(capture.query, name) {
			t.Fatalf("missing usage field %s", name)
		}
	}
	assertNoMySQL56IncompatibleSQL(t, capture.query)
}

func TestMySQL305DaybreakConditionalUpsert(t *testing.T) {
	raw := `{"upstream_type":"codex","account_id":"mysql305"}`
	capture := &mysqlCaptureDriver{queryRow: []driver.Value{raw}}
	db := newMySQLCaptureDB(t, capture)
	snapshot := DaybreakSnapshot{Identity: daybreakRowIdentity(raw), ObservedAt: 2, CheckedAt: 3, Models: map[string][]string{"gpt-6": {"daybreak_blue"}}}
	if err := db.SaveDaybreakSnapshot(context.Background(), 1, snapshot); err != nil {
		t.Fatal(err)
	}
	assertNoMySQL56IncompatibleSQL(t, capture.query)
	if !strings.Contains(capture.query, "observed_at=GREATEST(observed_at, VALUES(observed_at))") || strings.Contains(capture.query, "ON DUPLICATE KEY UPDATE identity=VALUES") {
		t.Fatalf("lost stale snapshot guard: %s", capture.query)
	}
	if len(capture.args) != 5 || capture.args[0].Value != snapshot.Identity || capture.args[4].Value != int64(1) {
		t.Fatalf("wrong INSERT SELECT parameter order: %+v", capture.args)
	}
}

func TestMySQL305Integration(t *testing.T) {
	dsn := os.Getenv("CODEX2API_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("requires an isolated MySQL database")
	}
	ctx := context.Background()
	db, err := New("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Repeated init must not fail on existing columns or indexes.
	for i := 0; i < 2; i++ {
		if err = db.InitImageJobQueue(ctx); err != nil {
			t.Fatal(err)
		}
		if err = db.InitImageMaintenance(ctx); err != nil {
			t.Fatal(err)
		}
		if err = db.ensureImageAssetRetentionSchema(ctx); err != nil {
			t.Fatal(err)
		}
		if err = db.createChannelMonitorSchema(ctx); err != nil {
			t.Fatal(err)
		}
	}
	previous, err := db.GetSystemSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if previous != nil {
			_ = db.UpdateSystemSettings(ctx, previous)
		} else {
			_, _ = db.conn.ExecContext(ctx, "DELETE FROM system_settings WHERE id=1")
		}
	}()
	s := &SystemSettings{CodexBasispointsEnabled: true, CodexBasispointsModels: "gpt-6", CodexBasispoints403PauseDisabled: true, CodexBasispointsProbeMinutes: 7, CodexBasispoints429CooldownSeconds: 13, CodexBasispointsCacheWriteAsInput: true, AutoResetCreditsOnExhaustionEnabled: true, CodexFingerprintDefaultMode: "single_machine_multi_window"}
	if err = db.UpdateSystemSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetSystemSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.CodexFingerprintDefaultMode != "single_machine_multi_window" || !got.CodexBasispointsEnabled || got.CodexBasispointsModels != "gpt-6" || !got.CodexBasispoints403PauseDisabled || got.CodexBasispointsProbeMinutes != 7 || got.CodexBasispoints429CooldownSeconds != 13 || !got.CodexBasispointsCacheWriteAsInput || !got.AutoResetCreditsOnExhaustionEnabled {
		t.Fatalf("settings roundtrip: %+v", got)
	}
	if err = db.UpdateCodexSyncedAppBuild(ctx, "desktop-windows", "12345"); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetSystemSettings(ctx)
	if err != nil || got.CodexSyncedDesktopWindowsBuild != "12345" {
		t.Fatalf("build marker: %+v %v", got, err)
	}
	jobID, err := db.InsertImageGenerationJob(ctx, ImageGenerationJobInput{Prompt: "mysql305 queue", ParamsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM image_assets WHERE job_id=$1", jobID)
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM image_generation_jobs WHERE id=$1", jobID)
	}()
	until := time.Now().Add(time.Minute)
	if ok, e := db.ClaimImageJob(ctx, jobID, "mysql305-owner", until); e != nil || !ok {
		t.Fatalf("queue claim: %v %v", ok, e)
	}
	if ok, e := db.ClaimImageJob(ctx, jobID, "other-owner", until); e != nil || ok {
		t.Fatalf("duplicate queue claim: %v %v", ok, e)
	}
	expires := time.Now().Add(time.Hour).Unix()
	assetID, err := db.InsertImageAsset(ctx, ImageAssetInput{JobID: jobID, Filename: "mysql305-test.png", ExpiresAt: expires, DeleteAfterRead: true})
	if err != nil {
		t.Fatal(err)
	}
	asset, err := db.GetImageAsset(ctx, assetID)
	if err != nil || asset.ExpiresAt != expires || !asset.DeleteAfterRead {
		t.Fatalf("asset retention: %+v %v", asset, err)
	}
	assets, err := db.DueImageAssets(ctx, time.Unix(expires, 0), assetID-1, 1)
	if err != nil || len(assets) != 1 || assets[0].ID != assetID {
		t.Fatalf("due assets: %+v %v", assets, err)
	}
	result, err := db.conn.ExecContext(ctx, `INSERT INTO accounts(name,credentials) VALUES($1,$2)`, "mysql305-test", `{"upstream_type":"codex","account_id":"mysql305"}`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM usage_logs WHERE account_id=$1", id)
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM accounts WHERE id=$1", id)
	}()
	now := time.Now().UTC().Truncate(time.Second)
	if _, err = db.UpsertChannelMonitorConfig(ctx, id, true, 5, "gpt-6", now); err != nil {
		t.Fatal(err)
	}
	if err = db.RecordChannelMonitorHealth(ctx, ChannelMonitorHealthResult{AccountID: id, Status: ChannelMonitorStatusOperational, Model: "gpt-6", CheckedAt: now, NextCheckAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err = db.RecordChannelMonitorBilling(ctx, ChannelMonitorBillingResult{AccountID: id, Status: "ok", Data: `{"balance":10}`, CheckedAt: now, NextCheckAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	cfg, err := db.GetChannelMonitorConfig(ctx, id)
	if err != nil || cfg.Status != ChannelMonitorStatusOperational || cfg.BillingStatus != "ok" {
		t.Fatalf("monitor: %+v, %v", cfg, err)
	}
	snap := DaybreakSnapshot{Identity: daybreakRowIdentity(`{"upstream_type":"codex","account_id":"mysql305"}`), ObservedAt: 20, CheckedAt: 21, Models: map[string][]string{"gpt-6": {"daybreak_blue"}}}
	if err = db.SaveDaybreakSnapshot(ctx, id, snap); err != nil {
		t.Fatal(err)
	}
	snap.ObservedAt = 10
	snap.Models = map[string][]string{}
	if err = db.SaveDaybreakSnapshot(ctx, id, snap); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.LoadDaybreakSnapshot(ctx, id)
	if err != nil || loaded.ObservedAt != 20 || loaded.CheckedAt != 21 || len(loaded.Models) != 1 {
		t.Fatalf("stale overwrite: %+v %v", loaded, err)
	}
	if err = db.batchInsertLogsChunk(ctx, db.conn, []usageLogEntry{{AccountID: id, StoreUsageLog: true, Model: "gpt-6", EffectiveModel: "gpt-6", UpstreamResponseModel: "gpt-6", UpstreamModelMismatch: boolPointer(true), DaybreakProgram: "daybreak_blue", VideoSeconds: 5, SessionID: "mysql305-session", RequestText: "local trace", InjectedTurnState: "injected", UpstreamTurnState: "observed", TurnStateOverridden: true}}); err != nil {
		t.Fatal(err)
	}
	page, err := db.ListUsageLogsByTimeRangePaged(ctx, UsageLogFilter{Start: now.Add(-time.Hour), End: now.Add(time.Hour), AccountID: &id, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Logs) != 1 || page.Logs[0].DaybreakProgram != "daybreak_blue" || page.Logs[0].VideoSeconds != 5 || page.Logs[0].UpstreamResponseModel != "gpt-6" || page.Logs[0].InjectedTurnState != "injected" || page.Logs[0].UpstreamModelMismatch == nil || !*page.Logs[0].UpstreamModelMismatch {
		t.Fatalf("usage roundtrip: %+v", page)
	}
}

func boolPointer(v bool) *bool { return &v }
