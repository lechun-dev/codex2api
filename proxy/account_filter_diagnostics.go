package proxy

import (
	"sync"

	"github.com/codex2api/auth"
)

const accountFilterDiagnosticLimit = 256

type accountFilterStageObservation struct {
	Passed    int  `json:"passed"`
	Rejected  int  `json:"rejected"`
	Truncated bool `json:"truncated"`
}

type accountFilterTrace struct {
	mu        sync.Mutex
	stages    map[string]map[int64]bool
	truncated map[string]bool
}

// 2026-09-08 coder(lq): Observe existing filter calls without replaying filters
// or changing short-circuit order. Results are cumulative per stage, not
// exclusive rejection reasons; unvisited accounts are intentionally absent.
func (t *accountFilterTrace) wrap(stage string, filter auth.AccountFilter) auth.AccountFilter {
	return func(account *auth.Account) bool {
		passed := filter == nil || filter(account)
		if account == nil {
			return passed
		}
		t.mu.Lock()
		if t.stages == nil {
			t.stages = make(map[string]map[int64]bool)
			t.truncated = make(map[string]bool)
		}
		results := t.stages[stage]
		if results == nil {
			results = make(map[int64]bool)
			t.stages[stage] = results
		}
		if _, exists := results[account.DBID]; exists || len(results) < accountFilterDiagnosticLimit {
			results[account.DBID] = passed
		} else {
			t.truncated[stage] = true
		}
		t.mu.Unlock()
		return passed
	}
}

func (t *accountFilterTrace) snapshot() map[string]accountFilterStageObservation {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]accountFilterStageObservation, len(t.stages))
	for stage, results := range t.stages {
		observation := accountFilterStageObservation{Truncated: t.truncated[stage]}
		for _, passed := range results {
			if passed {
				observation.Passed++
			} else {
				observation.Rejected++
			}
		}
		out[stage] = observation
	}
	return out
}
