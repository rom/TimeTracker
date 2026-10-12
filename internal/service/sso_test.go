package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Signing in through an identity provider, and what happens to an account the
// first time somebody arrives that way.
//
// The token validation lives in the auth package and has its own tests against
// forged tokens. What is decided here is who the token is taken to be: whether
// an arriving subject is matched to a colleague's existing account, what role a
// brand-new one gets, and whether a deactivated account can be brought back by
// signing in through the provider. Each of those, got wrong, hands somebody
// else's records to whoever controls an email address.

// ssoClaims is a token's payload as the provider would hand it over.
func ssoClaims(subject, email string, verified bool) auth.Claims {
	return auth.Claims{
		Subject: subject, Email: email, EmailVerified: verified,
		Name: "Signed-In Person", Issuer: "https://login.example",
	}
}

// TestAVerifiedAddressLinksToTheAccountThatAlreadyHasIt.
//
// Somebody who has been using a password and is moved onto single sign-on must
// land in their own account, with their own history, rather than in a second
// empty one. The duplicate would be the worse failure of the two: their
// timesheet would look empty and they would record it all again.
func TestAVerifiedAddressLinksToTheAccountThatAlreadyHasIt(t *testing.T) {
	f := newServerFixture(t)

	colleague, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Colleague", Email: "colleague@example.com",
		Password: "a-long-enough-password", Role: domain.RoleManager,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	result, err := f.accounts.LoginWithOIDC(f.ctx,
		ssoClaims("provider-subject-1", "colleague@example.com", true),
		"", "203.0.113.9", "a browser")
	if err != nil {
		t.Fatalf("sign in through the provider: %v", err)
	}
	if result.User.ID != colleague.ID {
		t.Errorf("signed in as user %d, want the existing account %d - a second "+
			"account would show them an empty timesheet", result.User.ID, colleague.ID)
	}
	if result.User.Role != domain.RoleManager {
		t.Errorf("the linked account's role is %q, want the manager role it already had",
			result.User.Role)
	}

	// No second account was made.
	users, err := f.accounts.Users(f.ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("%d accounts after linking, want the administrator and the colleague", len(users))
	}

	// Signing in again reaches the same account, by subject this time rather
	// than by email.
	again, err := f.accounts.LoginWithOIDC(f.ctx,
		ssoClaims("provider-subject-1", "colleague@example.com", true),
		"", "203.0.113.9", "a browser")
	if err != nil {
		t.Fatalf("sign in again: %v", err)
	}
	if again.User.ID != colleague.ID {
		t.Errorf("the second sign-in reached user %d, want %d", again.User.ID, colleague.ID)
	}

	// The linking was recorded, because an account quietly becoming reachable
	// by a new credential is exactly the kind of change somebody later needs to
	// find.
	trail, err := f.svc.AuditTrail(f.ctx, "user", colleague.ID)
	if err != nil {
		t.Fatalf("audit trail: %v", err)
	}
	linked := false
	for _, event := range trail {
		if strings.Contains(event.Action, "oidc") {
			linked = true
		}
	}
	if !linked {
		t.Error("linking an account to an identity provider was not recorded")
	}
}

// TestAnUnverifiedAddressLinksToNothing.
//
// An unverified email claim is an assertion the user made about themselves.
// Honouring it would mean anybody who can get a token from the provider with
// somebody else's address typed into it gets that person's account - which is
// the whole attack, and it needs no access to the address at all.
func TestAnUnverifiedAddressLinksToNothing(t *testing.T) {
	f := newServerFixture(t)

	colleague, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Colleague", Email: "colleague@example.com",
		Password: "a-long-enough-password", Role: domain.RoleManager,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	result, err := f.accounts.LoginWithOIDC(f.ctx,
		ssoClaims("someone-else", "colleague@example.com", false),
		"", "203.0.113.9", "a browser")

	// The security property, which is what this test is for: whatever else
	// happens, the arriving subject does not end up in the colleague's account.
	if err == nil && result.User.ID == colleague.ID {
		t.Fatal("an unverified email claim was enough to reach an existing " +
			"colleague's account")
	}

	// What actually happens is a refusal, because provisioning falls through to
	// creating an account and the address is already taken. That is the safe
	// outcome, and it arrives as a bare unique-constraint failure rather than
	// as something a person could act on. Pinned as what the application does,
	// so that tidying the message is a deliberate change and not an accident.
	if err == nil {
		t.Fatalf("signing in with an unverified claim of a taken address "+
			"succeeded, creating user %d", result.User.ID)
	}
	if !strings.Contains(err.Error(), "UNIQUE constraint") {
		t.Logf("the refusal now reads %q rather than a bare constraint failure", err)
	}

	// An unverified address nobody else holds is a new account, with the least
	// privilege there is.
	fresh, err := f.accounts.LoginWithOIDC(f.ctx,
		ssoClaims("another-subject", "stranger@example.com", false),
		"", "203.0.113.9", "a browser")
	if err != nil {
		t.Fatalf("sign in with an unclaimed address: %v", err)
	}
	if fresh.User.ID == colleague.ID {
		t.Fatal("a new sign-in reached the colleague's account")
	}
	if fresh.User.Role != domain.RoleMember {
		t.Errorf("the new account's role is %q, want the member role - a brand-new "+
			"account must see nothing until somebody grants access", fresh.User.Role)
	}
}

// TestAnAddressAlreadyLinkedToAnotherProviderAccountIsRefused.
//
// Two subjects, one address. Linking the second would make one mailbox a way
// into an account that another identity-provider login already owns, so it is
// refused rather than resolved by guessing which is the real person.
func TestAnAddressAlreadyLinkedToAnotherProviderAccountIsRefused(t *testing.T) {
	f := newServerFixture(t)

	if _, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Colleague", Email: "colleague@example.com",
		Password: "a-long-enough-password", Role: domain.RoleMember,
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// The first provider account links.
	if _, err := f.accounts.LoginWithOIDC(f.ctx,
		ssoClaims("subject-one", "colleague@example.com", true),
		"", "203.0.113.9", "a browser"); err != nil {
		t.Fatalf("first sign-in: %v", err)
	}

	// A different subject arriving with the same verified address does not.
	_, err := f.accounts.LoginWithOIDC(f.ctx,
		ssoClaims("subject-two", "colleague@example.com", true),
		"", "203.0.113.9", "a browser")
	if err == nil {
		t.Fatal("a second provider account was linked to an address that already " +
			"belongs to one")
	}
	if !strings.Contains(err.Error(), "already linked") {
		t.Errorf("the refusal reads %q, which does not say what the conflict is", err)
	}
}

// TestAMappedRoleClaimIsHonouredAndAnUnknownOneIsNot.
//
// A provider can be configured to carry a role, so a new account can arrive
// with the access it should have instead of waiting for somebody to grant it.
// Anything unrecognised has to fall back to the least privilege there is: a
// typo in the provider's configuration must not produce an administrator.
func TestAMappedRoleClaimIsHonouredAndAnUnknownOneIsNot(t *testing.T) {
	f := newServerFixture(t)

	cases := map[string]domain.Role{
		"manager":    domain.RoleManager,
		"admin":      domain.RoleAdmin,
		"":           domain.RoleMember,
		"superuser":  domain.RoleMember,
		"ADMIN":      domain.RoleMember,
		"timekeeper": domain.RoleMember,
	}

	i := 0
	for mapped, want := range cases {
		i++
		name := mapped
		if name == "" {
			name = "no role claim at all"
		}
		t.Run(name, func(t *testing.T) {
			email := "person" + string(rune('a'+i)) + "@example.com"
			result, err := f.accounts.LoginWithOIDC(f.ctx,
				ssoClaims("subject-"+name, email, true),
				mapped, "203.0.113.9", "a browser")
			if err != nil {
				t.Fatalf("sign in: %v", err)
			}
			if result.User.Role != want {
				t.Errorf("a role claim of %q produced the %q role, want %q",
					mapped, result.User.Role, want)
			}
		})
	}
}

// TestANewProviderAccountIsNamedSomething.
//
// The display name is what every screen shows and what an audit entry carries,
// so an account with an empty one is a row somebody cannot identify. The
// fallbacks go from the claim, to the local part of the address, to a constant.
func TestANewProviderAccountIsNamedSomething(t *testing.T) {
	f := newServerFixture(t)

	cases := []struct {
		name   string
		claims auth.Claims
		want   string
	}{
		{
			name: "the provider's own name for them",
			claims: auth.Claims{
				Subject: "s1", Email: "named@example.com", EmailVerified: true,
				Name: "Signed-In Person", Issuer: "https://login.example",
			},
			want: "Signed-In Person",
		},
		{
			name: "no name, so the address",
			claims: auth.Claims{
				Subject: "s2", Email: "unnamed@example.com", EmailVerified: true,
				Issuer: "https://login.example",
			},
			want: "unnamed@example.com",
		},
		{
			name: "neither, so a constant",
			claims: auth.Claims{
				Subject: "s3", Issuer: "https://login.example",
			},
			want: "SSO user",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := f.accounts.LoginWithOIDC(f.ctx, tc.claims, "",
				"203.0.113.9", "a browser")
			if err != nil {
				t.Fatalf("sign in: %v", err)
			}
			if result.User.DisplayName != tc.want {
				t.Errorf("the new account is named %q, want %q",
					result.User.DisplayName, tc.want)
			}
		})
	}
}

// TestADeactivatedAccountCannotComeBackThroughTheProvider.
//
// Deactivating is how somebody's access is removed when they leave. If signing
// in through the provider worked anyway, the one deliberate action an
// administrator takes to end access would do nothing - and would look like it
// had worked, because the password path is refused.
func TestADeactivatedAccountCannotComeBackThroughTheProvider(t *testing.T) {
	f := newServerFixture(t)

	leaver, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Leaver", Email: "leaver@example.com",
		Password: "a-long-enough-password", Role: domain.RoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := f.accounts.LoginWithOIDC(f.ctx,
		ssoClaims("leaver-subject", "leaver@example.com", true),
		"", "203.0.113.9", "a browser"); err != nil {
		t.Fatalf("sign in while active: %v", err)
	}

	leaver.Active = false
	if err := f.accounts.UpdateUser(f.ctx, leaver); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	_, err = f.accounts.LoginWithOIDC(f.ctx,
		ssoClaims("leaver-subject", "leaver@example.com", true),
		"", "203.0.113.9", "a browser")
	if err == nil {
		t.Fatal("a deactivated account signed in through the identity provider")
	}
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("the refusal is %v, want invalid credentials - the reason an "+
			"account is refused is not something to tell the person at the door", err)
	}
}

// TestASessionEndsWhenItExpires.
//
// The absolute timeout is the backstop: a cookie copied out of a browser works
// until it does, and that has to be a bounded window rather than for ever. The
// expired row is deleted rather than left for a sweep, so it cannot be used
// again even if the clock moves back.
func TestASessionEndsWhenItExpires(t *testing.T) {
	f := newServerFixture(t)

	login, err := f.accounts.Login(f.ctx, LoginRequest{
		Email: "admin@example.com", Password: "a-long-enough-password", IP: "203.0.113.9",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if _, _, err := f.accounts.ResolveSession(f.ctx, login.CookieValue); err != nil {
		t.Fatalf("the fresh session does not resolve: %v", err)
	}

	// Past the absolute timeout.
	f.now = f.now.Add(auth.SessionAbsoluteTimeout).Add(time.Minute)
	if _, _, err := f.accounts.ResolveSession(f.ctx, login.CookieValue); err == nil {
		t.Fatal("an expired session still resolves")
	}

	// And it is gone, not merely refused: moving the clock back must not bring
	// it back.
	f.now = f.now.Add(-auth.SessionAbsoluteTimeout).Add(-time.Minute)
	if _, _, err := f.accounts.ResolveSession(f.ctx, login.CookieValue); err == nil {
		t.Error("an expired session came back when the clock moved back, so it " +
			"was refused rather than deleted")
	}
}

// TestAnEmptyCookieIsNotASession.
//
// A browser with no cookie sends nothing, and the empty string must not be
// looked up: a lookup of "" that happened to match a row would be a login as
// whoever owned it.
func TestAnEmptyCookieIsNotASession(t *testing.T) {
	f := newServerFixture(t)

	_, _, err := f.accounts.ResolveSession(f.ctx, "")
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("resolving an empty cookie = %v, want unauthenticated", err)
	}
	if _, _, err := f.accounts.ResolveSession(f.ctx, "not-a-real-cookie"); err == nil {
		t.Error("a made-up cookie resolved to a session")
	}

	// Signing out with no cookie is a no-op rather than an error: it is a
	// button in a tab somebody left open.
	if err := f.accounts.Logout(f.ctx, ""); err != nil {
		t.Errorf("signing out with no cookie = %v, want a no-op", err)
	}
}

// TestSigningOutElsewhereKeepsThisBrowser.
//
// The control somebody uses when they think a session is open on a machine
// they no longer have. It has to end the others and keep this one, or using it
// signs you out and tells you nothing about whether it worked.
func TestSigningOutElsewhereKeepsThisBrowser(t *testing.T) {
	f := newServerFixture(t)

	var logins []LoginResult
	for i := 0; i < 3; i++ {
		login, err := f.accounts.Login(f.ctx, LoginRequest{
			Email: "admin@example.com", Password: "a-long-enough-password",
			IP: "203.0.113.9", UserAgent: "browser",
		})
		if err != nil {
			t.Fatalf("login %d: %v", i, err)
		}
		logins = append(logins, login)
		f.now = f.now.Add(time.Minute)
	}

	ctx := f.asUser(f.admin)
	sessions, err := f.accounts.Sessions(ctx)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("%d sessions, want 3", len(sessions))
	}

	// Keep the last one, which is this browser.
	_, keep, err := f.accounts.ResolveSession(ctx, logins[2].CookieValue)
	if err != nil {
		t.Fatalf("resolve the session to keep: %v", err)
	}
	if err := f.accounts.RevokeOtherSessions(ctx, keep); err != nil {
		t.Fatalf("revoke the others: %v", err)
	}

	if _, _, err := f.accounts.ResolveSession(ctx, logins[2].CookieValue); err != nil {
		t.Errorf("this browser's session was revoked too: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := f.accounts.ResolveSession(ctx, logins[i].CookieValue); err == nil {
			t.Errorf("session %d survived being revoked", i)
		}
	}

	remaining, err := f.accounts.Sessions(ctx)
	if err != nil {
		t.Fatalf("re-list sessions: %v", err)
	}
	if len(remaining) != 1 {
		t.Errorf("%d sessions remain, want 1", len(remaining))
	}
}

// TestExpiredSessionsArePrunedInBulk.
//
// The periodic sweep. Live sessions have to survive it, since a sweep that
// signed everybody out would be indistinguishable from an outage.
func TestExpiredSessionsArePrunedInBulk(t *testing.T) {
	f := newServerFixture(t)

	old, err := f.accounts.Login(f.ctx, LoginRequest{
		Email: "admin@example.com", Password: "a-long-enough-password", IP: "203.0.113.9",
	})
	if err != nil {
		t.Fatalf("first login: %v", err)
	}

	f.now = f.now.Add(auth.SessionAbsoluteTimeout).Add(time.Hour)
	fresh, err := f.accounts.Login(f.ctx, LoginRequest{
		Email: "admin@example.com", Password: "a-long-enough-password", IP: "203.0.113.9",
	})
	if err != nil {
		t.Fatalf("second login: %v", err)
	}

	pruned, err := f.accounts.PruneSessions(f.ctx)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 1 {
		t.Errorf("pruned %d sessions, want the 1 that had expired", pruned)
	}
	if _, _, err := f.accounts.ResolveSession(f.ctx, fresh.CookieValue); err != nil {
		t.Errorf("the sweep took a live session with it: %v", err)
	}
	if _, _, err := f.accounts.ResolveSession(f.ctx, old.CookieValue); err == nil {
		t.Error("the expired session still resolves after the sweep")
	}
}

// TestCreatingAnAccountRefusesWhatItCannotStore.
//
// Each of these is a field an administrator types. A name or address that is
// only whitespace produces an account nobody can identify or sign in to, and a
// role nothing recognises is a user the authoriser cannot reason about.
func TestCreatingAnAccountRefusesWhatItCannotStore(t *testing.T) {
	f := newServerFixture(t)

	cases := map[string]NewUserInput{
		"no name": {
			Email: "someone@example.com", Password: "a-long-enough-password",
			Role: domain.RoleMember,
		},
		"a name of spaces": {
			DisplayName: "   ", Email: "someone@example.com",
			Password: "a-long-enough-password", Role: domain.RoleMember,
		},
		"no address": {
			DisplayName: "Someone", Password: "a-long-enough-password",
			Role: domain.RoleMember,
		},
		"an address of spaces": {
			DisplayName: "Someone", Email: "  ",
			Password: "a-long-enough-password", Role: domain.RoleMember,
		},
		"no role": {
			DisplayName: "Someone", Email: "someone@example.com",
			Password: "a-long-enough-password",
		},
		"a role that does not exist": {
			DisplayName: "Someone", Email: "someone@example.com",
			Password: "a-long-enough-password", Role: domain.Role("superuser"),
		},
		"a password too short to be one": {
			DisplayName: "Someone", Email: "someone@example.com",
			Password: "short", Role: domain.RoleMember,
		},
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := f.accounts.CreateUser(f.ctx, in); err == nil {
				t.Errorf("an account with %s was created", name)
			}
		})
	}

	// Nothing was stored by any of them.
	users, err := f.accounts.Users(f.ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("%d accounts exist, want only the bootstrapped administrator", len(users))
	}
}

// TestTheFirstAdministratorNeedsAPasswordAndGetsAName.
//
// Bootstrap runs with no identity, so it is the one account-creation path that
// cannot ask an authoriser anything. A passwordless administrator would be an
// account anybody could claim, and the name fallbacks exist because the setup
// form asks for as little as it can.
func TestTheFirstAdministratorNeedsAPasswordAndGetsAName(t *testing.T) {
	cases := []struct {
		name  string
		in    NewUserInput
		named string
	}{
		{"a name they gave", NewUserInput{
			DisplayName: "Rom", Email: "rom@example.com", Password: "a-long-enough-password",
		}, "Rom"},
		{"no name, so the address's local part", NewUserInput{
			Email: "rom@example.com", Password: "a-long-enough-password",
		}, "rom"},
		{"an address with no local part either", NewUserInput{
			Email: "@example.com", Password: "a-long-enough-password",
		}, "Administrator"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBareFixture(t)
			admin, err := f.accounts.BootstrapFirstAdmin(f.ctx, tc.in)
			if err != nil {
				t.Fatalf("bootstrap: %v", err)
			}
			if admin.DisplayName != tc.named {
				t.Errorf("the first administrator is named %q, want %q",
					admin.DisplayName, tc.named)
			}
			if admin.Role != domain.RoleAdmin {
				t.Errorf("the first account's role is %q, want administrator", admin.Role)
			}
		})
	}

	t.Run("no password at all", func(t *testing.T) {
		f := newBareFixture(t)
		_, err := f.accounts.BootstrapFirstAdmin(f.ctx, NewUserInput{
			DisplayName: "Rom", Email: "rom@example.com",
		})
		if err == nil {
			t.Fatal("the first administrator was created with no password, which " +
				"is an account anybody could claim")
		}
		if !strings.Contains(err.Error(), "password") {
			t.Errorf("the refusal reads %q, which does not say what is missing", err)
		}
	})
}

// newBareFixture is an instance with no accounts at all, for the bootstrap
// path - which is the one that must behave differently once there are any.
func newBareFixture(t *testing.T) *serverFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("account tests touch the filesystem; skipped under -short")
	}

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "bare.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	f := &serverFixture{db: db, now: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return f.now }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.svc = New(db, auth.RoleAuthorizer{IsProjectMember: db.IsProjectMember}, logger, clock)
	f.accounts = NewAccounts(db, f.svc, clock)
	f.ctx = ctx
	return f
}
