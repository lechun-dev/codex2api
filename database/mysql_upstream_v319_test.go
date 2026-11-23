package database

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ic "github.com/codex2api/internal/intelligencecheck"
)

// 2026-11-23 coder(lq): Guard fresh/upgrade DDL and source-aware queries even when no MySQL server is available locally.
func TestMySQL319SchemaAndExpressions(t *testing.T) {
	for _, ddl := range keyUsageAccountMySQLDDL() {
		assertNoMySQL56IncompatibleSQL(t, ddl)
		if strings.Contains(ddl, "TEXT PRIMARY KEY") || strings.Contains(ddl, "0900") {
			t.Fatalf("unbounded or unsupported primary-key definition: %s", ddl)
		}
	}
	db := &DB{driver: "mysql"}
	for _, expr := range []string{db.intelligenceSourceEnabledSQL("acc", "cfg"), db.intelligenceAutomaticSQL("acc", "st")} {
		assertNoMySQL56IncompatibleSQL(t, expr)
		if strings.Contains(expr, "a.credentials") {
			t.Fatalf("account alias was not replaced: %s", expr)
		}
	}
}

func TestMySQL319PortalRejectsMalformedCredentialJSON(t *testing.T) {
	capture := &mysqlCaptureDriver{queryRow: []driver.Value{int64(9), "malformed", "{"}}
	db := newMySQLCaptureDB(t, capture)
	if _, err := db.ListKeyUsageAccountAccess(context.Background()); err == nil {
		t.Fatal("invalid credentials must not become an unrestricted portal account")
	}
}

// 2026-11-23 coder(lq): CI runs this on both MySQL 5.6 and 8.0; it uses an isolated schema, never a live service database.
func TestMySQL319Integration(t *testing.T) {
	db, err := New("mysql", mysqlTempDatabase(t, mysqlIntelligenceDSN(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.UpdateSystemSettings(ctx, &SystemSettings{SiteName: "MySQL 319 test"}); err != nil {
		t.Fatal(err)
	}
	settings, err := db.GetSystemSettings(ctx)
	if err != nil || settings == nil {
		t.Fatalf("settings on fresh schema: %+v %v", settings, err)
	}
	settings.BackgroundQuietHours = `[{"start":"23:00","end":"06:00"}]`
	if err := db.UpdateSystemSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	enabled := true
	guard, err := db.UpdateSystemSettingsWithOutputGuard(ctx, settings, OutputGuardSettingsUpdate{Enabled: &enabled})
	if err != nil || guard.Enabled == nil || !*guard.Enabled {
		t.Fatalf("output guard: %+v %v", guard, err)
	}
	if err := db.SaveKiroSettings(ctx, KiroSettings{APIDomain: "kiro", CacheSimulation: true}); err != nil {
		t.Fatal(err)
	}
	if got, err := db.LoadKiroSettings(ctx); err != nil || got.APIDomain != "kiro" || !got.CacheSimulation {
		t.Fatalf("kiro settings: %+v %v", got, err)
	}
	// Exercise the actual old-schema upgrade path, not only CREATE IF NOT EXISTS.
	if _, err := db.conn.ExecContext(ctx, `ALTER TABLE system_settings DROP COLUMN background_quiet_hours`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.migrateMySQL(ctx); err != nil {
			t.Fatalf("idempotent upgrade %d: %v", i, err)
		}
	}
	if got, err := db.GetSystemSettings(ctx); err != nil || got.BackgroundQuietHours != "[]" {
		t.Fatalf("upgraded missing TEXT setting uses reader default: %+v %v", got, err)
	}

	id, err := db.InsertAccountWithCredentials(ctx, "兼容测试 Kiro", map[string]interface{}{
		"upstream_type": "kiro", "kiro_auth_kind": "social", "kiro_login_source": "google",
		"kiro_usage": map[string]interface{}{"used": 5}, "email": "mysql319@example.test",
		"plan_type": "pro", "allowed_api_key_ids": []int64{3, 7}, "access_token": "test-secret-never-projected",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"kiro", "codex"} {
		full, err := db.ListActiveByChannel(ctx, channel)
		if err != nil {
			t.Fatal(err)
		}
		projection, err := db.ListAccountListProjection(ctx, channel)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if channel == "kiro" {
			want = 1
		}
		if len(full) != want || len(projection) != want {
			t.Fatalf("%s full=%d projection=%d want=%d", channel, len(full), len(projection), want)
		}
		if want == 1 {
			public := projection[0].Credentials
			if public["kiro_auth_kind"] != "social" || public["kiro_usage"] == nil || public["access_token"] != nil {
				t.Fatalf("wrong allowlist projection: keys=%v", public)
			}
		}
	}
	access, err := db.ListKeyUsageAccountAccess(ctx)
	if err != nil || len(access[id].AllowedAPIKeyIDs) != 2 || access[id].Email != "mysql319@example.test" {
		t.Fatalf("portal projection: %+v %v", access, err)
	}
	if err := db.SetKeyUsageAccountPermissions(ctx, 3, KeyUsageAccountPermissions{ViewEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetKeyUsageAccountPermissions(ctx, 3); err != nil || !got.ViewEnabled || got.ResetEnabled {
		t.Fatalf("portal permissions: %+v %v", got, err)
	}
	for i := 0; i < 2; i++ {
		if ok, err := db.ReserveKeyUsageReset(ctx, "319-reset-uuid", 3, id); err != nil || ok != (i == 0) {
			t.Fatalf("idempotency attempt %d: %t %v", i, ok, err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	var leases, cooldowns atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if ok, err := db.AcquireKeyUsageResetLease(ctx, "account:319", fmt.Sprintf("owner-%d", i), now.Unix(), now.Unix()+60); err != nil {
				t.Error(err)
			} else if ok {
				leases.Add(1)
			}
			if ok, err := db.ReserveKeyUsageResetCooldown(ctx, "account:319", now.Unix(), now.Unix()+60); err != nil {
				t.Error(err)
			} else if ok {
				cooldowns.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if leases.Load() != 1 || cooldowns.Load() != 1 {
		t.Fatalf("concurrent reservations leases=%d cooldowns=%d", leases.Load(), cooldowns.Load())
	}
	if ok, err := db.AcquireKeyUsageResetLease(ctx, "account:319", "next", now.Unix()+61, now.Unix()+120); err != nil || !ok {
		t.Fatalf("expired lease must be reclaimable: %t %v", ok, err)
	}
	if err := db.ReleaseKeyUsageResetLease(ctx, "account:319", "wrong-owner"); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.AcquireKeyUsageResetLease(ctx, "account:319", "other", now.Unix()+62, now.Unix()+121); err != nil || ok {
		t.Fatalf("wrong owner must not release a lease: %t %v", ok, err)
	}
	if err := db.RecordKeyUsageResetCooldown(ctx, "account:319", now.Unix()+180); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordKeyUsageResetCooldown(ctx, "account:319", now.Unix()+30); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ReserveKeyUsageResetCooldown(ctx, "account:319", now.Unix()+70, now.Unix()+200); err != nil || ok {
		t.Fatalf("cooldown must not move backwards: %t %v", ok, err)
	}
	if err := db.InsertUsageLog(ctx, &UsageLogInput{AccountID: id, Endpoint: "/v1/responses", Model: "test", StatusCode: 200, TotalTokens: 9, CreatedAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	db.FlushUsageLogs()
	usage, err := db.GetAccountUsageSinceWindows(ctx, map[int64]time.Time{id: now.Add(-2 * time.Minute)})
	if err != nil || usage[id] == nil || usage[id].Tokens != 9 {
		t.Fatalf("per-account official window: %+v %v", usage, err)
	}
	if _, err := db.GetTrafficSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	keyID, err := db.InsertAPIKey(ctx, "中文 key", "sk-319-test-only")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.usageAPIKeyMetadata(ctx, []int64{keyID, keyID + 1}); err != nil {
		t.Fatalf("deleted/live key labels: %v", err)
	}
	if _, err := db.ListAPIKeyTokenStats(ctx, now.Add(-time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatalf("token ranking aggregation: %v", err)
	}

	check, err := db.GetIntelligenceCheckSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check.Channels = map[string]bool{"kiro": true, "codex": false, "claude": false}
	check, err = db.SaveIntelligenceCheckSettings(ctx, check)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetIntelligenceCheckEnabled(ctx, []int64{id}, true); err != nil {
		t.Fatal(err)
	}
	query := `SELECT ` + db.intelligenceAutomaticSQL("a", "st") + ` FROM accounts a JOIN intelligence_check_accounts st ON st.account_id=a.id WHERE a.id=$1`
	for _, tc := range []struct {
		generation int64
		allowed    bool
	}{{0, true}, {1, false}, {10, true}} {
		state := ic.State{}
		if tc.generation != 0 {
			generation := tc.generation
			state.BlockedCredentialGeneration = &generation
		}
		raw, _ := json.Marshal(state)
		if _, err := db.conn.ExecContext(ctx, `UPDATE intelligence_check_accounts SET state_json=$1 WHERE account_id=$2`, string(raw), id); err != nil {
			t.Fatal(err)
		}
		var allowed bool
		if err := db.conn.QueryRowContext(ctx, query, id).Scan(&allowed); err != nil || allowed != tc.allowed {
			t.Fatalf("generation %d: allowed=%t want=%t err=%v", tc.generation, allowed, tc.allowed, err)
		}
	}
	if _, err := db.IntelligenceCheckBars(ctx, []int64{id}); err != nil {
		t.Fatalf("source-aware bars: %v", err)
	}
}
