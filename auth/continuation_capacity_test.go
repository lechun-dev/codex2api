package auth

import (
	"sync/atomic"
	"testing"

	"github.com/codex2api/database"
)

func TestReplayableContinuationOnlyBorrowsForCapacity(t *testing.T) {
	for _, mode := range []string{"capacity", "available", "filtered", "fallback_filtered", "missing"} {
		t.Run(mode, func(t *testing.T) {
			s := NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1})
			defer s.Stop()
			bound := &Account{DBID: 1, UpstreamType: UpstreamOpenAIResponses, BaseURL: "https://example.test", APIKey: "bound", PlanType: "api"}
			other := &Account{DBID: 2, UpstreamType: UpstreamOpenAIResponses, BaseURL: "https://example.test", APIKey: "other", PlanType: "api"}
			s.AddAccount(bound)
			s.AddAccount(other)
			if mode != "missing" {
				s.BindSessionAffinity("turn", bound, "")
			}
			if mode != "available" {
				atomic.StoreInt64(&bound.ActiveRequests, 1)
				atomic.StoreInt64(&bound.OccupiedRequests, 1)
			}
			filter := func(a *Account) bool {
				return !(mode == "filtered" && a.ID() == 1) && !(mode == "fallback_filtered" && a.ID() == 2)
			}
			excluded := map[int64]bool{}
			got, _, _, borrowed := s.NextReplayableContinuationWithDispatch("turn", 0, excluded, filter, DispatchPolicyStandard)
			if len(excluded) != 0 {
				t.Fatal("mutated caller exclusions")
			}
			switch mode {
			case "capacity":
				if got != other || !borrowed {
					t.Fatalf("got=%v borrowed=%v", got, borrowed)
				}
			case "available":
				if got != bound || borrowed {
					t.Fatalf("got=%v borrowed=%v", got, borrowed)
				}
			default:
				if got != nil || borrowed {
					t.Fatal("borrow bypassed eligibility")
				}
			}
			if got != nil {
				s.Release(got)
			}
		})
	}
}
