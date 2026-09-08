package proxy

import (
	"testing"

	"github.com/codex2api/auth"
)

func TestAccountFilterTracePreservesShortCircuit(t *testing.T) {
	trace := &accountFilterTrace{}
	calls := 0
	inner := trace.wrap("inner", func(a *auth.Account) bool { calls++; return a.DBID == 1 })
	outer := trace.wrap("outer", func(a *auth.Account) bool { return a.DBID != 3 && inner(a) })
	for id := int64(1); id <= 3; id++ {
		if got := outer(&auth.Account{DBID: id}); got != (id == 1) {
			t.Fatalf("id=%d result=%v", id, got)
		}
	}
	got := trace.snapshot()
	if calls != 2 || got["inner"].Passed != 1 || got["inner"].Rejected != 1 || got["outer"].Rejected != 2 {
		t.Fatalf("calls=%d observations=%v", calls, got)
	}
}

func TestAccountFilterTraceBoundsAndReplacesObservations(t *testing.T) {
	trace := &accountFilterTrace{}
	pass := true
	filter := trace.wrap("test", func(*auth.Account) bool { return pass })
	for id := int64(1); id <= accountFilterDiagnosticLimit+1; id++ {
		filter(&auth.Account{DBID: id})
	}
	pass = false
	filter(&auth.Account{DBID: 1})
	got := trace.snapshot()["test"]
	if !got.Truncated || got.Passed != accountFilterDiagnosticLimit-1 || got.Rejected != 1 {
		t.Fatalf("%+v", got)
	}
	if !trace.wrap("nil", nil)(nil) {
		t.Fatal("nil filter changed result")
	}
}
