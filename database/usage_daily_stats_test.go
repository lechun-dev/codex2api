package database

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestGetDailyTokenUsageSQLiteAggregatesByDayAndModel(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "daily-usage.sqlite"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	insert := func(createdAt time.Time, model, effectiveModel, channel string, apiKeyID, accountID, statusCode, total, input, output, cached int64) {
		t.Helper()
		_, err := db.conn.ExecContext(ctx, `INSERT INTO usage_logs
			(model, effective_model, channel, api_key_id, account_id, status_code,
			 total_tokens, input_tokens, output_tokens, cached_tokens, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			model, effectiveModel, channel, apiKeyID, accountID, statusCode,
			total, input, output, cached, db.timeArg(createdAt))
		if err != nil {
			t.Fatalf("insert usage log: %v", err)
		}
	}

	dayOne := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	dayTwo := dayOne.Add(24 * time.Hour)
	insert(dayOne, "gpt-requested", "gpt-effective", UpstreamChannelCodex, 7, 11, 200, 100, 40, 50, 10)
	insert(dayOne.Add(2*time.Hour), "gpt-requested", "gpt-effective", UpstreamChannelCodex, 7, 11, 200, 50, 20, 25, 5)
	insert(dayOne.Add(3*time.Hour), "other", "", UpstreamChannelCodex, 8, 12, 200, 25, 10, 12, 3)
	insert(dayTwo, "ignored", "ignored", UpstreamChannelCodex, 7, 11, 499, 999, 0, 0, 0)
	insert(dayTwo, "gpt-requested", "gpt-effective", UpstreamChannelGrok, 7, 11, 200, 80, 30, 45, 5)

	start := dayOne
	end := dayTwo.Add(24 * time.Hour)
	stats, err := db.GetDailyTokenUsage(ctx, start, end, UpstreamChannelCodex, "", nil, nil)
	if err != nil {
		t.Fatalf("GetDailyTokenUsage(): %v", err)
	}
	if got, want := stats.Models, []string{"gpt-effective", "other"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Models = %#v, want %#v", got, want)
	}
	if len(stats.Rows) != 1 {
		t.Fatalf("len(Rows) = %d, want 1", len(stats.Rows))
	}
	row := stats.Rows[0]
	if row.Date != "2026-08-10" || row.Requests != 3 || row.TotalTokens != 175 {
		t.Fatalf("row = %#v, want date 2026-08-10, requests 3, total 175", row)
	}
	if row.ModelTokens["gpt-effective"] != 150 || row.ModelTokens["other"] != 25 {
		t.Fatalf("row.ModelTokens = %#v", row.ModelTokens)
	}
	if stats.Total.Requests != 3 || stats.Total.TotalTokens != 175 || stats.Total.CachedTokens != 18 {
		t.Fatalf("total = %#v", stats.Total)
	}

	apiKeyID := int64(7)
	filtered, err := db.GetDailyTokenUsage(ctx, start, end, UpstreamChannelCodex, "gpt-effective", &apiKeyID, nil)
	if err != nil {
		t.Fatalf("GetDailyTokenUsage(filtered): %v", err)
	}
	if filtered.Total.Requests != 2 || filtered.Total.TotalTokens != 150 {
		t.Fatalf("filtered total = %#v, want requests 2 and tokens 150", filtered.Total)
	}
}
