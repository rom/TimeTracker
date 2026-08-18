package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// ---------------------------------------------------------------- passwords --

func TestPasswordRoundTrip(t *testing.T) {
	const password = "correct horse battery staple"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	// The stored form must be self-describing, so a future version with
	// different defaults can still verify it.
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=") {
		t.Fatalf("unexpected hash format: %q", hash)
	}
	if err := VerifyPassword(password, hash); err != nil {
		t.Errorf("correct password rejected: %v", err)
	}
	if err := VerifyPassword("wrong password entirely", hash); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password: got %v, want ErrInvalidCredentials", err)
	}
}

// TestPasswordHashesAreSalted: two users with the same password must not share a
// hash, or a stolen database reveals which accounts to attack together.
func TestPasswordHashesAreSalted(t *testing.T) {
	const password = "the same password twice"

	first, err := HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if first == second {
		t.Error("identical passwords produced identical hashes: the salt is not random")
	}
}

func TestPasswordPolicy(t *testing.T) {
	if _, err := HashPassword("short"); !errors.Is(err, ErrValidationPassword) {
		t.Errorf("a short password should be refused, got %v", err)
	}
	if _, err := HashPassword(strings.Repeat("a", 2000)); !errors.Is(err, ErrValidationPassword) {
		// An unbounded input turns deliberately expensive hashing into a weapon.
		t.Errorf("an over-long password should be refused, got %v", err)
	}
}

// TestMalformedHashIsRejected: a corrupted or truncated hash must never verify.
func TestMalformedHashIsRejected(t *testing.T) {
	for _, bad := range []string{
		"", "not a hash", "$argon2id$", "$bcrypt$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=99$m=1,t=1,p=1$c2FsdA$aGFzaA",
	} {
		if err := VerifyPassword("anything", bad); err == nil {
			t.Errorf("malformed hash %q verified successfully", bad)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	current, err := HashPassword("a perfectly fine password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if NeedsRehash(current) {
		t.Error("a hash made with the current defaults should not need rehashing")
	}

	// A hash made with weaker parameters must be upgraded on next login.
	weak, err := hashPasswordWith("a perfectly fine password", argon2Params{
		memoryKiB: 1024, iterations: 1, parallelism: 1, saltLength: 16, keyLength: 32,
	})
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !NeedsRehash(weak) {
		t.Error("a hash with weaker parameters should need rehashing")
	}
	// It must still verify in the meantime, with its own parameters.
	if err := VerifyPassword("a perfectly fine password", weak); err != nil {
		t.Errorf("an old hash should still verify: %v", err)
	}
}

// ----------------------------------------------------------------- sessions --

func TestSessionIDsAreUniqueAndHashed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		value, hash, err := NewSessionID()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if seen[value] {
			t.Fatal("session id repeated")
		}
		seen[value] = true

		if hash == value {
			t.Fatal("the stored hash equals the cookie value; a stolen database would yield usable cookies")
		}
		if HashSessionID(value) != hash {
			t.Fatal("hashing is not deterministic")
		}
	}
}

func TestSessionExpiry(t *testing.T) {
	now := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	session := Session{
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(SessionAbsoluteTimeout),
	}

	if session.Expired(now) {
		t.Error("a fresh session should not be expired")
	}
	// Idle expiry, independent of the absolute lifetime.
	if !session.Expired(now.Add(SessionIdleTimeout + time.Minute)) {
		t.Error("a session idle beyond the timeout should be expired")
	}
	// Absolute expiry, even with continued use.
	used := Session{
		CreatedAt:  now,
		LastSeenAt: now.Add(SessionAbsoluteTimeout + time.Hour),
		ExpiresAt:  now.Add(SessionAbsoluteTimeout),
	}
	if !used.Expired(now.Add(SessionAbsoluteTimeout + time.Hour)) {
		t.Error("a session past its absolute lifetime should be expired despite recent use")
	}
}

// ------------------------------------------------------------ rate limiting --

func TestLoginLimiterBacksOff(t *testing.T) {
	now := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter(func() time.Time { return now })

	// The first few failures are free: people mistype passwords.
	for i := 0; i < loginFreeAttempts; i++ {
		if allowed, _ := limiter.Allowed("account:a"); !allowed {
			t.Fatalf("attempt %d should still be allowed", i+1)
		}
		limiter.Failed("account:a")
	}

	allowed, wait := limiter.Allowed("account:a")
	if allowed {
		t.Fatal("the limiter should engage after the free attempts are used")
	}
	if wait <= 0 {
		t.Error("a lockout should report how long to wait")
	}

	// A different account is unaffected: one user's failures must not lock out
	// everybody.
	if allowed, _ := limiter.Allowed("account:b"); !allowed {
		t.Error("an unrelated account was locked out")
	}

	// Waiting clears it.
	now = now.Add(time.Hour)
	if allowed, _ := limiter.Allowed("account:a"); !allowed {
		t.Error("the lockout should lapse")
	}
}

func TestLoginLimiterClearsOnSuccess(t *testing.T) {
	now := time.Now()
	limiter := NewLoginLimiter(func() time.Time { return now })

	for i := 0; i < loginFreeAttempts+3; i++ {
		limiter.Failed("account:a")
	}
	if allowed, _ := limiter.Allowed("account:a"); allowed {
		t.Fatal("should be locked out")
	}

	limiter.Succeeded("account:a")
	if allowed, _ := limiter.Allowed("account:a"); !allowed {
		t.Error("a successful login should clear the counter")
	}
}

// ----------------------------------------------------------------- the RBAC --

// TestRoleMatrix is the exhaustive check behind ASR-005: every role against
// every action, in and out of scope.
//
// It is written as an explicit table rather than generated, because this is the
// security policy: a reader should be able to see the whole of it and disagree
// with a line.
func TestRoleMatrix(t *testing.T) {
	const memberProject = int64(1)
	const otherProject = int64(2)
	const ownCustomer = int64(10)
	const otherCustomer = int64(20)

	authorizer := RoleAuthorizer{
		IsProjectMember: func(_ context.Context, userID, projectID int64) (bool, error) {
			// Every test user is a member of exactly project 1.
			return projectID == memberProject, nil
		},
	}

	const actorID = int64(7)
	user := func(role domain.Role) domain.User {
		return domain.User{ID: actorID, Role: role, Active: true, ClientCustomerID: ownCustomer}
	}

	ownEntry := Resource{Type: "time_entry", OwnerID: actorID, ProjectID: memberProject}
	colleagueEntryInScope := Resource{Type: "time_entry", OwnerID: 99, ProjectID: memberProject}
	colleagueEntryOutOfScope := Resource{Type: "time_entry", OwnerID: 99, ProjectID: otherProject}
	ownCustomerResource := Resource{Type: "report", CustomerID: ownCustomer}
	otherCustomerResource := Resource{Type: "report", CustomerID: otherCustomer}

	tests := []struct {
		name     string
		role     domain.Role
		action   Action
		resource Resource
		allowed  bool
	}{
		// Admin: everything.
		{"admin manages users", domain.RoleAdmin, ActionManage, Resource{Type: "user"}, true},
		{"admin reads any project", domain.RoleAdmin, ActionView, colleagueEntryOutOfScope, true},
		{"admin approves", domain.RoleAdmin, ActionApprove, colleagueEntryOutOfScope, true},

		// Manager: real authority, bounded by membership.
		{"manager reads their team's entry", domain.RoleManager, ActionView, colleagueEntryInScope, true},
		{"manager approves in their project", domain.RoleManager, ActionApprove, colleagueEntryInScope, true},
		{"manager sees money in their project", domain.RoleManager, ActionViewMoney, colleagueEntryInScope, true},
		{"manager may propose for their team", domain.RoleManager, ActionProxy, colleagueEntryInScope, true},
		{"manager cannot read another project", domain.RoleManager, ActionView, colleagueEntryOutOfScope, false},
		{"manager cannot approve outside their projects", domain.RoleManager, ActionApprove, colleagueEntryOutOfScope, false},

		// Member: own records, plus read access to their projects.
		{"member reads own entry", domain.RoleMember, ActionView, ownEntry, true},
		{"member edits own entry", domain.RoleMember, ActionUpdate, ownEntry, true},
		{"member deletes own entry", domain.RoleMember, ActionDelete, ownEntry, true},
		{"member reads a colleague in a shared project", domain.RoleMember, ActionView, colleagueEntryInScope, true},
		{"member may propose for a colleague on a shared project", domain.RoleMember, ActionProxy, colleagueEntryInScope, true},
		{"member cannot edit a colleague's entry", domain.RoleMember, ActionUpdate, colleagueEntryInScope, false},
		{"member cannot delete a colleague's entry", domain.RoleMember, ActionDelete, colleagueEntryInScope, false},
		{"member cannot read another project", domain.RoleMember, ActionView, colleagueEntryOutOfScope, false},
		{"member cannot propose outside their projects", domain.RoleMember, ActionProxy, colleagueEntryOutOfScope, false},
		{"member cannot administer", domain.RoleMember, ActionManage, Resource{Type: "user"}, false},
		{"member cannot approve", domain.RoleMember, ActionApprove, ownEntry, false},
		{"member cannot see a colleague's money", domain.RoleMember, ActionViewMoney, colleagueEntryInScope, false},

		// Client: read-only, one customer.
		{"client reads its own customer", domain.RoleClient, ActionView, ownCustomerResource, true},
		{"client cannot read another customer", domain.RoleClient, ActionView, otherCustomerResource, true},
		{"client cannot write", domain.RoleClient, ActionCreate, ownCustomerResource, false},
		{"client cannot update", domain.RoleClient, ActionUpdate, ownCustomerResource, false},
		{"client cannot administer", domain.RoleClient, ActionManage, Resource{Type: "user"}, false},
		{"client cannot approve", domain.RoleClient, ActionApprove, ownCustomerResource, false},
		{"client with no customer scope is refused", domain.RoleClient, ActionView, Resource{Type: "report"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithUser(context.Background(), user(tc.role))
			err := authorizer.Can(ctx, tc.action, tc.resource)

			if tc.allowed && err != nil {
				t.Errorf("expected to be permitted, got %v", err)
			}
			if !tc.allowed && err == nil {
				t.Errorf("expected to be refused, but it was permitted")
			}
			if !tc.allowed && err != nil && !errors.Is(err, ErrForbidden) {
				t.Errorf("refusal should be ErrForbidden, got %v", err)
			}
		})
	}
}

// TestInactiveUserIsRefused: disabling an account must take effect on the very
// next authorisation decision, not merely at the login form.
func TestInactiveUserIsRefused(t *testing.T) {
	authorizer := RoleAuthorizer{
		IsProjectMember: func(context.Context, int64, int64) (bool, error) { return true, nil },
	}
	ctx := WithUser(context.Background(), domain.User{ID: 1, Role: domain.RoleAdmin, Active: false})

	if err := authorizer.Can(ctx, ActionView, Resource{Type: "time_entry"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("a disabled administrator should be refused, got %v", err)
	}
}

// TestMissingMembershipLookupFailsClosed: a misconfigured authoriser must refuse,
// never allow.
func TestMissingMembershipLookupFailsClosed(t *testing.T) {
	authorizer := RoleAuthorizer{} // no lookup injected
	ctx := WithUser(context.Background(), domain.User{ID: 1, Role: domain.RoleManager, Active: true})

	err := authorizer.Can(ctx, ActionView, Resource{Type: "time_entry", ProjectID: 5})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("a missing membership lookup should refuse, got %v", err)
	}
}

// TestUnknownRoleIsRefused: a role that arrives from a claim mapping or a
// tampered database must not fall through to permission.
func TestUnknownRoleIsRefused(t *testing.T) {
	authorizer := RoleAuthorizer{
		IsProjectMember: func(context.Context, int64, int64) (bool, error) { return true, nil },
	}
	ctx := WithUser(context.Background(), domain.User{ID: 1, Role: domain.Role("superuser"), Active: true})

	if err := authorizer.Can(ctx, ActionView, Resource{Type: "time_entry"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("an unknown role should be refused, got %v", err)
	}
}

// TestUnauthenticatedIsRefused: no identity, no access - in either authoriser.
func TestUnauthenticatedIsRefused(t *testing.T) {
	bare := context.Background()

	if err := (RoleAuthorizer{}).Can(bare, ActionView, Resource{}); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("RoleAuthorizer: got %v, want ErrUnauthenticated", err)
	}
	if err := (SingleUserAuthorizer{}).Can(bare, ActionView, Resource{}); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("SingleUserAuthorizer: got %v, want ErrUnauthenticated", err)
	}
}

// TestSingleUserAuthorizerStillChecksOwnership: local mode is a permissive
// authoriser, not an absent one.
func TestSingleUserAuthorizerStillChecksOwnership(t *testing.T) {
	ctx := WithUser(context.Background(), domain.User{ID: 1, Role: domain.RoleAdmin, Active: true})

	if err := (SingleUserAuthorizer{}).Can(ctx, ActionUpdate,
		Resource{Type: "time_entry", OwnerID: 1}); err != nil {
		t.Errorf("own record refused: %v", err)
	}
	if err := (SingleUserAuthorizer{}).Can(ctx, ActionUpdate,
		Resource{Type: "time_entry", OwnerID: 2}); !errors.Is(err, ErrForbidden) {
		t.Errorf("another user's record should be refused even locally, got %v", err)
	}
}
