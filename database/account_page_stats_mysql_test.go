package database

import (
	"reflect"
	"strings"
	"testing"
)

func TestAppendAccountIDFilterMySQLUsesExpandedPlaceholders(t *testing.T) {
	db := &DB{driver: "mysql"}
	args := []interface{}{"since"}

	filter := db.appendAccountIDFilter(&args, []int64{3, 5})

	if filter != "account_id IN ($2,$3)" {
		t.Fatalf("filter = %q, want expanded MySQL placeholders", filter)
	}
	if strings.Contains(filter, "ANY(") {
		t.Fatalf("MySQL filter contains PostgreSQL ANY syntax: %s", filter)
	}
	wantArgs := []interface{}{"since", int64(3), int64(5)}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}
