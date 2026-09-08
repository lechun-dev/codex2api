package auth

import (
	"bytes"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUnavailablePoolSnapshotClassifiesLocalGates(t *testing.T) {
	accounts := make([]*Account, 7)
	for i := range accounts {
		accounts[i] = newFastSchedulerTestAccount(int64(i+1), HealthTierHealthy, 100, 2)
	}
	accounts[0].Disabled = 1
	accounts[1].DispatchPaused = 1
	accounts[2].Status = StatusCooldown
	accounts[2].CooldownUtil = time.Now().Add(time.Hour)
	accounts[3].AccessToken = ""
	accounts[4].OccupiedRequests = 2
	accounts[5].DynamicConcurrencyLimit = 0
	s := &Store{accounts: accounts, maxConcurrency: 2}
	got := s.unavailablePoolSnapshot(0, nil, DispatchPolicyStandard)
	for _, reason := range []string{"disabled", "dispatch_paused", "cooldown", "missing_credential", "cached_capacity_full", "cached_concurrency_zero"} {
		if got.Reasons[reason] != 1 {
			t.Errorf("reason %s: snapshot=%+v", reason, got)
		}
	}
	if got.Total != 7 || got.LocalCandidates != 1 || got.Active != 0 || got.Occupied != 2 || got.Buffered != 2 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if accounts[4].OccupiedRequests != 2 || accounts[5].DynamicConcurrencyLimit != 0 {
		t.Fatal("diagnostics mutated scheduling state")
	}
	got = s.unavailablePoolSnapshot(0, map[int64]bool{7: true}, DispatchPolicyStandard)
	if got.LocalCandidates != 0 || got.Reasons["excluded_by_request"] != 1 {
		t.Fatalf("exclusion not reported: %+v", got)
	}
}

func TestUnavailablePoolLogIsSampledAndDoesNotLogCredentials(t *testing.T) {
	acc := newFastSchedulerTestAccount(1, HealthTierHealthy, 100, 2)
	acc.AccessToken = "secret-must-not-appear"
	s := &Store{accounts: []*Account{acc}, maxConcurrency: 2}
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(old) })
	s.LogUnavailablePool("req-test", "test-model", 42, nil, DispatchPolicyStandard)
	s.LogUnavailablePool("req-suppressed", "test-model", 42, nil, DispatchPolicyStandard)
	text := output.String()
	if !strings.Contains(text, "req-test") || strings.Contains(text, "req-suppressed") || strings.Contains(text, acc.AccessToken) {
		t.Fatalf("unexpected diagnostic log: %s", text)
	}
	if atomic.LoadInt64(&acc.ActiveRequests) != 0 || atomic.LoadInt64(&acc.TotalRequests) != 0 {
		t.Fatal("diagnostics acquired an account")
	}
}
