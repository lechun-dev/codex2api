package auth

import (
	"strings"
	"time"
)

// 2026-09-08 coder(lq): Only borrow capacity after the bound account passes all
// eligibility checks; callers must first validate that the request is replayable.
func (s *Store) NextReplayableContinuationWithDispatch(key string, apiKeyID int64, exclude map[int64]bool, filter AccountFilter, policy DispatchPolicy) (*Account, string, SessionAffinityGuard, bool) {
	if s == nil {
		return nil, "", SessionAffinityGuard{}, false
	}
	key = strings.TrimSpace(key)
	s.sessionMu.RLock()
	binding, ok := s.sessionBindings[key]
	s.sessionMu.RUnlock()
	if !ok {
		binding, ok = s.getCachedSessionAffinity(key)
	}
	if !ok || !binding.expiresAt.After(time.Now()) || !s.affinityProxyStillValid(binding.accountID, binding.proxyURL) {
		return nil, "", SessionAffinityGuard{}, false
	}
	id := binding.accountID
	account, full := s.takeByIDModeWithCapacity(id, apiKeyID, exclude, filter, true, key, policy)
	if account != nil {
		s.sessionMu.Lock()
		if current, exists := s.sessionBindings[key]; exists && current.accountID == id {
			current.requestCount++
			current.lastUsedAt = time.Now()
			s.sessionBindings[key] = current
		}
		s.sessionMu.Unlock()
		return account, binding.proxyURL, SessionAffinityGuard{}, false
	}
	if !full {
		return nil, "", SessionAffinityGuard{}, false
	}
	borrowExclude := make(map[int64]bool, len(exclude)+1)
	for k, v := range exclude {
		borrowExclude[k] = v
	}
	borrowExclude[id] = true
	account = s.nextAccountForFreshAffinityWithDispatch(key, apiKeyID, borrowExclude, filter, policy)
	if account == nil {
		return nil, "", SessionAffinityGuard{}, false
	}
	return account, "", SessionAffinityGuard{preserveAccountID: id}, true
}
