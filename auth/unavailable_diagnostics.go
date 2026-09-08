package auth

import (
	"encoding/json"
	"log"
	"sync/atomic"
	"time"
)

type unavailablePoolSnapshot struct {
	Total           int            `json:"total"`
	Reasons         map[string]int `json:"first_blocking_reason"`
	Active          int64          `json:"active_requests"`
	Occupied        int64          `json:"occupied_requests"`
	Buffered        int64          `json:"buffered_or_pending_slots"`
	LocalCandidates int            `json:"candidates_before_request_filters"`
}

// 2026-09-08 coder(lq): Only inspect local state; replaying request filters can
// mutate scope-budget decisions, and reading Redis during an outage adds latency.
func (s *Store) unavailablePoolSnapshot(apiKeyID int64, exclude map[int64]bool, policy DispatchPolicy) unavailablePoolSnapshot {
	out := unavailablePoolSnapshot{Reasons: make(map[string]int)}
	now := time.Now()
	for _, acc := range s.accountSnapshotAccounts() {
		if acc == nil {
			continue
		}
		out.Total++
		active := atomic.LoadInt64(&acc.ActiveRequests)
		occupied := atomic.LoadInt64(&acc.OccupiedRequests)
		if occupied < active {
			occupied = active
		}
		out.Active += active
		out.Occupied += occupied
		out.Buffered += occupied - active
		reason := ""
		switch {
		case exclude[acc.DBID]:
			reason = "excluded_by_request"
		case !acc.AllowsAPIKey(apiKeyID):
			reason = "account_api_key_binding"
		case atomic.LoadInt32(&acc.Disabled) != 0:
			reason = "disabled"
		case atomic.LoadInt32(&acc.DispatchPaused) != 0:
			reason = "dispatch_paused"
		default:
			acc.mu.RLock()
			switch {
			case acc.dispatchableForPolicyLocked(now, policy):
				// 2026-09-08 coder(lq): This is the cached standard concurrency limit,
				// not a replay of dynamic/Spark admission. Report it as an observation.
				if policy == DispatchPolicyStandard && acc.DynamicConcurrencyLimit <= 0 {
					reason = "cached_concurrency_zero"
				} else if policy == DispatchPolicyStandard && occupied >= acc.DynamicConcurrencyLimit {
					reason = "cached_capacity_full"
				}
			case acc.Status == StatusError:
				reason = "status_error"
			case acc.healthTierLocked() == HealthTierBanned:
				reason = "health_banned"
			case !acc.hasDispatchCredentialLocked():
				reason = "missing_credential"
			case acc.Status == StatusCooldown && now.Before(acc.CooldownUtil):
				reason = "cooldown"
			case policy == DispatchPolicyStandard && acc.usageWindowBlocksFreshDispatchLocked(now):
				reason = "usage_window"
			case policy == DispatchPolicyStandard && acc.quotaAutoPausedLocked(now):
				reason = "quota_auto_pause"
			default:
				reason = "other_local_gate"
			}
			acc.mu.RUnlock()
		}
		if reason == "" {
			out.LocalCandidates++
		} else {
			out.Reasons[reason]++
		}
	}
	return out
}

// 2026-09-08 coder(lq): Sample at most once per ten seconds per store, before
// scanning the pool. Never include credentials, user names or request content.
func (s *Store) LogUnavailablePool(requestID, model string, apiKeyID int64, exclude map[int64]bool, policy DispatchPolicy) {
	if s == nil {
		return
	}
	now := time.Now().UnixNano()
	last := s.unavailableLogNS.Load()
	if now-last < int64(10*time.Second) || !s.unavailableLogNS.CompareAndSwap(last, now) {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"event": "account_pool_unavailable", "request_id": requestID,
		"model": model, "api_key_id": apiKeyID, "scheduler_engine": s.SchedulerEngine(),
		"policy": policy, "base_concurrency": atomic.LoadInt64(&s.maxConcurrency),
		"snapshot": s.unavailablePoolSnapshot(apiKeyID, exclude, policy),
		"scope":    "local_cached_state_only; group/model/egress/Redis/continuation constraints not evaluated",
	})
	if err == nil {
		log.Printf("[account_pool_unavailable] %s", payload)
	}
}
