package store

import (
	"strings"
	"testing"
)

// The scope is the shape of every listing in the application.
//
// It is one small function, and the whole of ADR-0008's listing half rests on
// it: a query that reads it wrongly either hides somebody's own work from them
// or shows them somebody else's. It is worth testing directly, as SQL, rather
// than only through the listings that use it - a listing test that passes
// because its fixture has no out-of-scope rows proves nothing.

// TestAScopeRendersWhatEachDimensionMeans.
func TestAScopeRendersWhatEachDimensionMeans(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scope    Scope
		project  string
		customer string
		owner    string
		want     string
		wantArgs []any
	}{
		{
			name:    "an administrator is unrestricted",
			scope:   UnrestrictedScope(),
			project: "a.project_id", customer: "p.customer_id", owner: "e.user_id",
			want: "",
		},
		{
			name:    "a client is their customer and nothing else",
			scope:   Scope{CustomerID: 10, OwnUserID: 7},
			project: "a.project_id", customer: "p.customer_id", owner: "e.user_id",
			// The owner is deliberately absent: a client's scope is a customer,
			// and the work they read is by definition somebody else's.
			want:     "p.customer_id = ?",
			wantArgs: []any{int64(10)},
		},
		{
			name:    "a member is their projects or their own records",
			scope:   Scope{ProjectIDs: []int64{1, 2}, OwnUserID: 7},
			project: "a.project_id", customer: "p.customer_id", owner: "e.user_id",
			want:     "(a.project_id IN (?,?) OR e.user_id = ?)",
			wantArgs: []any{int64(1), int64(2), int64(7)},
		},
		{
			name:    "a member where the query names no owner is their projects alone",
			scope:   Scope{ProjectIDs: []int64{1, 2}, OwnUserID: 7},
			project: "a.project_id", customer: "p.customer_id", owner: "",
			want:     "a.project_id IN (?,?)",
			wantArgs: []any{int64(1), int64(2)},
		},
		{
			name:    "a member with no memberships is their own records",
			scope:   Scope{OwnUserID: 7},
			project: "a.project_id", customer: "p.customer_id", owner: "e.user_id",
			// The case that was broken: this rendered "1 = 0", so somebody who
			// had just recorded an hour found every screen empty.
			want:     "e.user_id = ?",
			wantArgs: []any{int64(7)},
		},
		{
			name:    "a member with no memberships and no owner column sees nothing",
			scope:   Scope{OwnUserID: 7},
			project: "a.project_id", customer: "p.customer_id", owner: "",
			// Always-false rather than empty, which would mean "everything".
			want: "1 = 0",
		},
		{
			name:    "an unknown role sees nothing",
			scope:   Scope{},
			project: "a.project_id", customer: "p.customer_id", owner: "e.user_id",
			want: "1 = 0",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, args := tc.scope.condition(tc.project, tc.customer, tc.owner)
			if got != tc.want {
				t.Errorf("condition = %q, want %q", got, tc.want)
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("condition bound %v, want %v", args, tc.wantArgs)
			}
			for i := range args {
				if args[i] != tc.wantArgs[i] {
					t.Errorf("argument %d is %v, want %v", i, args[i], tc.wantArgs[i])
				}
			}
			// However it is rendered, it is parameterised: the placeholder count
			// must match the arguments, or the query is either malformed or
			// carrying a value somebody typed.
			if strings.Count(got, "?") != len(args) {
				t.Errorf("condition %q has %d placeholders and %d arguments",
					got, strings.Count(got, "?"), len(args))
			}
		})
	}
}

// TestAWideningScopeStaysParenthesised.
//
// The clause is ANDed with every other condition in its query, so an OR without
// brackets would not widen the scope - it would discard the rest of the query.
// "this week, on my projects, or mine" is a very different listing from "this
// week on my projects, or everything of mine ever".
func TestAWideningScopeStaysParenthesised(t *testing.T) {
	scope := Scope{ProjectIDs: []int64{1}, OwnUserID: 7}
	got, _ := scope.condition("a.project_id", "p.customer_id", "e.user_id")

	if !strings.Contains(got, " OR ") {
		t.Fatalf("condition = %q, want the widening form", got)
	}
	if !strings.HasPrefix(got, "(") || !strings.HasSuffix(got, ")") {
		t.Errorf("condition = %q; an OR clause ANDed into a query must be "+
			"bracketed or it swallows the query's other conditions", got)
	}
}
