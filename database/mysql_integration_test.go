package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"
)

func TestMySQLIntegrationSmoke(t *testing.T) {
	dsn := os.Getenv("CODEX2API_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set CODEX2API_MYSQL_TEST_DSN to run MySQL integration smoke test")
	}

	ctx := context.Background()
	db, err := New("mysql", dsn)
	if err != nil {
		t.Fatalf("New(mysql) failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	smokeKey := "sk-mysql-smoke-" + suffix
	groupName := "mysql-smoke-group-" + suffix
	proxyURL := "http://127.0.0.1:18080/" + suffix
	modelID := "mysql-smoke-model-" + suffix
	templateName := "mysql-smoke-template-" + suffix
	clientUserAgent := "mysql-smoke-client/" + suffix
	previousSettings, err := db.GetSystemSettings(ctx)
	if err != nil {
		t.Fatalf("GetSystemSettings before smoke failed: %v", err)
	}
	previousPromptFilterSecret, secretErr := db.GetPromptFilterNewAPISecret(ctx)
	hadPromptFilterSecret := secretErr == nil
	if secretErr != nil && !errors.Is(secretErr, sql.ErrNoRows) {
		t.Fatalf("GetPromptFilterNewAPISecret before smoke failed: %v", secretErr)
	}
	t.Cleanup(func() {
		if previousSettings != nil {
			_ = db.UpdateSystemSettings(ctx, previousSettings)
		} else {
			_, _ = db.conn.ExecContext(ctx, "DELETE FROM system_settings WHERE id = 1")
		}
		if hadPromptFilterSecret {
			_ = db.SetPromptFilterNewAPISecret(ctx, previousPromptFilterSecret)
		} else {
			_, _ = db.conn.ExecContext(ctx, "DELETE FROM prompt_filter_secrets WHERE id = 1")
		}
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM usage_logs WHERE client_user_agent = ?", clientUserAgent)
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM account_group_members WHERE group_id IN (SELECT id FROM account_groups WHERE name = ?)", groupName)
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM image_prompt_templates WHERE name = ?", templateName)
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM model_registry WHERE id = ?", modelID)
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM proxies WHERE url = ?", proxyURL)
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM account_groups WHERE name = ?", groupName)
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM api_keys WHERE `key` = ?", smokeKey)
		_, _ = db.conn.ExecContext(ctx, "DELETE FROM accounts WHERE name IN (?, ?)", "mysql smoke account "+suffix, "mysql smoke responses "+suffix)
	})

	settings := &SystemSettings{
		SiteName:                           "mysql smoke " + suffix,
		SiteLogo:                           "https://example.test/logo.png",
		BackgroundConfig:                   `{"image":"https://example.test/bg.png","opacity":20}`,
		MaxConcurrency:                     3,
		GlobalRPM:                          11,
		TestModel:                          "gpt-5.4",
		TestConcurrency:                    7,
		ProxyURL:                           proxyURL,
		PgMaxConns:                         12,
		RedisPoolSize:                      13,
		AdminSecret:                        "mysql-smoke-secret",
		BackgroundRefreshIntervalMinutes:   4,
		UsageProbeMaxAgeMinutes:            5,
		UsageProbeConcurrency:              6,
		UsageProbeResponsesFallbackEnabled: false,
		RecoveryProbeIntervalMinutes:       8,
		ModelMapping:                       "{}",
		CodexModelMapping:                  "{}",
		PayloadRules:                       `{"append":[{"path":"instructions","value":"mysql smoke"}]}`,
		ReasoningEffortModels:              "[]",
		PromptFilterStrictTerminalEnabled:  true,
		PromptFilterAdvancedConfig:         `{"normalization":{"enabled":true}}`,
		ClientCompatMode:                   "responses",
		CodexMinCLIVersion:                 "0.119.0",
		CodexUserAgentConfig:               `{"terminal":"xterm-256color","os_name":"Linux","os_version":"Unknown"}`,
		UsageLogMode:                       "minimal",
		UsageLogBatchSize:                  33,
		UsageLogFlushIntervalSeconds:       9,
		StreamFlushPolicy:                  "buffered",
		StreamFlushIntervalMS:              44,
		FirstTokenTimeoutSeconds:           55,
		BillingTierPolicy:                  "requested",
		ImageStorageConfig:                 `{"backend":"local"}`,
		ShowFullUsageNumbers:               true,
		PublicImageStudioPageEnabled:       true,
		PublicAccountPortalPageEnabled:     true,
		SchedulerMode:                      "weighted",
		AffinityMode:                       "strict",
		CodexForceWebsocket:                true,
		CodexWSKeepaliveEnabled:            true,
		CodexWSKeepaliveIntervalSec:        66,
		CodexWSHideUpstreamErrors:          false,
		CodexWSSilentRetryEnabled:          false,
		CodexWSSilentMaxRetries:            4,
		ModelPricingOverrides:              `{"gpt-5.4":{"input":2.5,"source":"custom"}}`,
		ModelPricingSyncURL:                "https://example.test/pricing.json",
		IgnoreUsageLimitStatus:             true,
		AutoResetCreditsEnabled:            true,
		AutoResetCreditsBeforeExpiryMin:    75,
	}
	if err := db.UpdateSystemSettings(ctx, settings); err != nil {
		t.Fatalf("UpdateSystemSettings failed: %v", err)
	}
	if err := db.UpdateModelPricingSettings(ctx, settings.ModelPricingOverrides, settings.ModelPricingSyncURL); err != nil {
		t.Fatalf("UpdateModelPricingSettings failed: %v", err)
	}
	savedSettings, err := db.GetSystemSettings(ctx)
	if err != nil {
		t.Fatalf("GetSystemSettings failed: %v", err)
	}
	if savedSettings == nil {
		t.Fatal("GetSystemSettings returned nil")
	}
	if savedSettings.FirstTokenTimeoutSeconds != 55 ||
		savedSettings.StreamFlushPolicy != "buffered" ||
		savedSettings.StreamFlushIntervalMS != 44 ||
		savedSettings.ClientCompatMode != "responses" ||
		savedSettings.CodexMinCLIVersion != "0.119.0" ||
		savedSettings.CodexUserAgentConfig != `{"terminal":"xterm-256color","os_name":"Linux","os_version":"Unknown"}` ||
		savedSettings.PayloadRules != `{"append":[{"path":"instructions","value":"mysql smoke"}]}` ||
		!savedSettings.PromptFilterStrictTerminalEnabled ||
		savedSettings.PromptFilterAdvancedConfig != `{"normalization":{"enabled":true}}` ||
		savedSettings.UsageLogMode != "minimal" ||
		savedSettings.UsageLogBatchSize != 33 ||
		savedSettings.UsageLogFlushIntervalSeconds != 9 ||
		savedSettings.SchedulerMode != "weighted" ||
		savedSettings.AffinityMode != "strict" ||
		savedSettings.CodexForceWebsocket != true ||
		savedSettings.CodexWSKeepaliveEnabled != true ||
		savedSettings.CodexWSKeepaliveIntervalSec != 66 ||
		savedSettings.CodexWSHideUpstreamErrors != false ||
		savedSettings.CodexWSSilentRetryEnabled != false ||
		savedSettings.CodexWSSilentMaxRetries != 4 ||
		savedSettings.ModelPricingOverrides != `{"gpt-5.4":{"input":2.5,"source":"custom"}}` ||
		savedSettings.ModelPricingSyncURL != "https://example.test/pricing.json" ||
		!savedSettings.PublicImageStudioPageEnabled ||
		!savedSettings.PublicAccountPortalPageEnabled ||
		!savedSettings.IgnoreUsageLimitStatus ||
		!savedSettings.AutoResetCreditsEnabled ||
		savedSettings.AutoResetCreditsBeforeExpiryMin != 75 {
		t.Fatalf("system settings were not persisted correctly: %#v", savedSettings)
	}

	promptFilterSecret := "mysql-smoke-prompt-filter-" + suffix
	if err := db.SetPromptFilterNewAPISecret(ctx, promptFilterSecret); err != nil {
		t.Fatalf("SetPromptFilterNewAPISecret failed: %v", err)
	}
	savedPromptFilterSecret, err := db.GetPromptFilterNewAPISecret(ctx)
	if err != nil {
		t.Fatalf("GetPromptFilterNewAPISecret failed: %v", err)
	}
	if savedPromptFilterSecret != promptFilterSecret {
		t.Fatalf("prompt filter secret = %q, want %q", savedPromptFilterSecret, promptFilterSecret)
	}

	keyID, err := db.InsertAPIKeyWithOptions(ctx, APIKeyInput{
		Name:            "mysql smoke key",
		Key:             smokeKey,
		QuotaLimit:      123.5,
		AllowedGroupIDs: []int64{2, 1, 2},
		Limits: APIKeyLimits{
			ModelAllow: []string{"gpt-5.4"},
			RPM:        10,
		},
	})
	if err != nil {
		t.Fatalf("InsertAPIKeyWithOptions failed: %v", err)
	}
	row, err := db.GetAPIKeyByValue(ctx, smokeKey)
	if err != nil {
		t.Fatalf("GetAPIKeyByValue failed: %v", err)
	}
	if row.ID != keyID || row.Key != smokeKey || len(row.AllowedGroupIDs) != 3 || row.Limits.RPM != 10 {
		t.Fatalf("unexpected API key row: %#v", row)
	}

	groupID, err := db.CreateAccountGroup(ctx, groupName, "integration", "#123456", 0, 0, sql.NullInt64{Int64: 6, Valid: true})
	if err != nil {
		t.Fatalf("CreateAccountGroup failed: %v", err)
	}
	if groupID <= 0 {
		t.Fatalf("groupID = %d, want positive", groupID)
	}
	groups, err := db.ListAccountGroups(ctx)
	if err != nil {
		t.Fatalf("ListAccountGroups failed: %v", err)
	}
	var foundGroup bool
	for _, group := range groups {
		if group.ID == groupID {
			foundGroup = true
			if !group.BaseConcurrencyOverride.Valid || group.BaseConcurrencyOverride.Int64 != 6 {
				t.Fatalf("group base_concurrency_override = %+v, want 6", group.BaseConcurrencyOverride)
			}
		}
	}
	if !foundGroup {
		t.Fatalf("created account group %d not found", groupID)
	}

	accountID, err := db.InsertAccountWithCredentials(ctx, "mysql smoke account "+suffix, map[string]interface{}{
		"refresh_token": "rt-smoke-" + suffix,
	}, proxyURL)
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials failed: %v", err)
	}
	if err := db.UpdateAccountSchedulerMetadata(ctx, accountID,
		OptionalNullInt64{Set: true, Value: sql.NullInt64{Int64: 17, Valid: true}},
		OptionalNullInt64{Set: true, Value: sql.NullInt64{Int64: 19, Valid: true}},
		OptionalBool{Set: true, Value: true},
		OptionalInt64Slice{Set: true, Values: []int64{keyID}},
		OptionalStringSlice{Set: true, Values: []string{"smoke", "mysql"}},
		OptionalInt64Slice{Set: true, Values: []int64{groupID}},
		OptionalString{Set: true, Value: proxyURL + "/updated"},
		map[string]interface{}{"auto_pause_5h_disabled": true},
	); err != nil {
		t.Fatalf("UpdateAccountSchedulerMetadata failed: %v", err)
	}
	if err := db.UpdateAccountNote(ctx, accountID, "mysql smoke note"); err != nil {
		t.Fatalf("UpdateAccountNote failed: %v", err)
	}
	account, err := db.GetAccountByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetAccountByID failed: %v", err)
	}
	if !account.ScoreBiasOverride.Valid || account.ScoreBiasOverride.Int64 != 17 ||
		!account.BaseConcurrencyOverride.Valid || account.BaseConcurrencyOverride.Int64 != 19 ||
		!account.SkipWarmTier ||
		account.ProxyURL != proxyURL+"/updated" ||
		account.Note != "mysql smoke note" ||
		!account.GetCredentialBool("auto_pause_5h_disabled") ||
		len(account.GetCredentialInt64Slice("allowed_api_key_ids")) != 1 ||
		account.GetCredentialInt64Slice("allowed_api_key_ids")[0] != keyID {
		t.Fatalf("account metadata was not persisted correctly: %#v", account)
	}

	if err := db.batchInsertLogs(ctx, []usageLogEntry{{
		AccountID:           accountID,
		ClientIP:            "127.0.0.1",
		SessionID:           "mysql-smoke-session",
		ConversationID:      "mysql-smoke-conversation",
		PreviousResponseID:  "mysql-smoke-response",
		RequestText:         "mysql smoke request",
		ClientUserAgent:     clientUserAgent,
		UpstreamUserAgent:   "mysql-smoke-upstream/" + suffix,
		UserAgentOverridden: true,
		Endpoint:            "/v1/responses",
		Model:               "gpt-5.4",
		StatusCode:          200,
	}}); err != nil {
		t.Fatalf("batchInsertLogs failed: %v", err)
	}
	var savedClientUA, savedUpstreamUA string
	var savedOverridden bool
	if err := db.conn.QueryRowContext(ctx, `
		SELECT client_user_agent, upstream_user_agent, user_agent_overridden
		FROM usage_logs
		WHERE client_user_agent = ?
	`, clientUserAgent).Scan(&savedClientUA, &savedUpstreamUA, &savedOverridden); err != nil {
		t.Fatalf("query usage-log audit fields failed: %v", err)
	}
	if savedClientUA != clientUserAgent || savedUpstreamUA != "mysql-smoke-upstream/"+suffix || !savedOverridden {
		t.Fatalf("unexpected usage-log audit fields: client=%q upstream=%q overridden=%v", savedClientUA, savedUpstreamUA, savedOverridden)
	}

	responsesID, err := db.InsertOpenAIResponsesAccount(ctx, "mysql smoke responses "+suffix, map[string]interface{}{
		"upstream_type": "openai_responses",
		"base_url":      "https://api.example.test",
		"api_key":       "sk-smoke-old",
		"models":        []string{"gpt-5.4"},
	}, proxyURL)
	if err != nil {
		t.Fatalf("InsertOpenAIResponsesAccount failed: %v", err)
	}
	if err := db.UpdateOpenAIResponsesAccount(ctx, responsesID, "mysql smoke responses "+suffix, map[string]interface{}{
		"upstream_type": "openai_responses",
		"base_url":      "https://api2.example.test",
		"api_key":       "sk-smoke-new",
		"models":        []string{"gpt-5.4", "gpt-5.5"},
		"plan_type":     "api",
	}, proxyURL+"/responses"); err != nil {
		t.Fatalf("UpdateOpenAIResponsesAccount failed: %v", err)
	}
	responsesAccount, err := db.GetAccountByID(ctx, responsesID)
	if err != nil {
		t.Fatalf("GetAccountByID responses failed: %v", err)
	}
	if responsesAccount.ProxyURL != proxyURL+"/responses" ||
		responsesAccount.GetCredential("base_url") != "https://api2.example.test" ||
		responsesAccount.GetCredential("api_key") != "sk-smoke-new" ||
		len(responsesAccount.GetCredentialStringSlice("models")) != 2 {
		t.Fatalf("responses account was not persisted correctly: %#v", responsesAccount)
	}

	if inserted, err := db.InsertProxies(ctx, []string{proxyURL, proxyURL}, "smoke"); err != nil {
		t.Fatalf("InsertProxies failed: %v", err)
	} else if inserted != 1 {
		t.Fatalf("InsertProxies inserted = %d, want 1", inserted)
	}

	if err := db.UpsertModelRegistryRows(ctx, []ModelRegistryRow{{
		ID:                  modelID,
		Enabled:             true,
		Category:            "codex",
		Source:              "integration",
		ProOnly:             false,
		APIKeyAuthAvailable: true,
		LastSeenAt:          sql.NullTime{Time: time.Now().UTC(), Valid: true},
	}}); err != nil {
		t.Fatalf("UpsertModelRegistryRows insert failed: %v", err)
	}
	if err := db.UpsertModelRegistryRows(ctx, []ModelRegistryRow{{
		ID:                  modelID,
		Enabled:             false,
		Category:            "codex",
		Source:              "integration-update",
		ProOnly:             true,
		APIKeyAuthAvailable: false,
		LastSeenAt:          sql.NullTime{Time: time.Now().UTC(), Valid: true},
	}}); err != nil {
		t.Fatalf("UpsertModelRegistryRows update failed: %v", err)
	}

	templateID, err := db.InsertImagePromptTemplate(ctx, ImagePromptTemplateInput{
		Name:   templateName,
		Prompt: "draw a production smoke test",
		Tags:   []string{"smoke", "mysql"},
	})
	if err != nil {
		t.Fatalf("InsertImagePromptTemplate failed: %v", err)
	}
	if templateID <= 0 {
		t.Fatalf("templateID = %d, want positive", templateID)
	}

	if snapshot, err := db.GetTrafficSnapshot(ctx); err != nil {
		t.Fatalf("GetTrafficSnapshot failed: %v", err)
	} else if snapshot == nil {
		t.Fatalf("GetTrafficSnapshot returned nil")
	}
}
