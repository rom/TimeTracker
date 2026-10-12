package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Signing in, in the cases that are not the happy path.
//
// Each of these is a line somebody could delete without any test noticing, and
// the consequence of each is somebody else's login rather than a wrong figure
// on a screen.

// TestGrindingAPasswordIsThrottled.
//
// The first few failures are free, because people mistype passwords. After
// that the delay grows, which is what makes an online guessing attack
// impractical - and the refusal says how long to wait rather than looking like
// another wrong password, since the person being throttled is usually the
// legitimate one.
func TestGrindingAPasswordIsThrottled(t *testing.T) {
	f := newServerFixture(t)

	attempt := func() error {
		_, err := f.accounts.Login(f.ctx, LoginRequest{
			Email: "admin@example.com", Password: "not-the-password", IP: "203.0.113.9",
		})
		return err
	}

	// The free attempts: refused, and not yet throttled.
	for i := 0; i < 5; i++ {
		err := attempt()
		if err == nil {
			t.Fatalf("attempt %d with a wrong password succeeded", i)
		}
		if strings.Contains(err.Error(), "too many") {
			t.Fatalf("attempt %d was already throttled; the first few failures "+
				"are meant to be free, because people mistype passwords", i)
		}
	}

	// Past the threshold, the refusal changes.
	err := attempt()
	if err == nil {
		t.Fatal("a sixth wrong password succeeded")
	}
	if !strings.Contains(err.Error(), "too many") {
		t.Errorf("the refusal after six failures reads %q, want it to say to wait", err)
	}
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("a throttled attempt returns %v; it must still be an invalid-"+
			"credentials error, or the endpoint says which accounts exist", err)
	}

	// The *correct* password is refused too while the lock holds: otherwise
	// the throttle would only slow somebody down until they guessed right,
	// which is exactly the moment it matters.
	if _, err := f.accounts.Login(f.ctx, LoginRequest{
		Email: "admin@example.com", Password: "a-long-enough-password", IP: "203.0.113.9",
	}); err == nil {
		t.Error("the right password went through while the account was locked out")
	}

	// Waiting out the window lets them in again, so a locked-out person is not
	// locked out for ever.
	f.now = f.now.Add(20 * time.Minute)
	if _, err := f.accounts.Login(f.ctx, LoginRequest{
		Email: "admin@example.com", Password: "a-long-enough-password", IP: "203.0.113.9",
	}); err != nil {
		t.Errorf("the right password is still refused after the window: %v", err)
	}

	// And a success clears the counter, so the next mistype starts from zero.
	if err := attempt(); err != nil && strings.Contains(err.Error(), "too many") {
		t.Error("the counter was not cleared by a successful sign-in")
	}
}

// TestAnAccountWithNoPasswordCannotBeSignedIntoWithOne.
//
// An account provisioned through the identity provider has no password hash.
// Falling through to a hash comparison against an empty string would be a
// disaster - depending on the verifier, an empty password might match - so it
// is refused explicitly, and refused in the same words as any other failure.
func TestAnAccountWithNoPasswordCannotBeSignedIntoWithOne(t *testing.T) {
	f := newServerFixture(t)

	// An account that exists only through the provider.
	if _, err := f.accounts.LoginWithOIDC(f.ctx, auth.Claims{
		Subject: "sso-only", Email: "sso@example.com", EmailVerified: true,
		Name: "SSO Person", Issuer: "https://login.example",
	}, "", "203.0.113.9", "a browser"); err != nil {
		t.Fatalf("provision through the provider: %v", err)
	}

	for _, password := range []string{"", " ", "password", "a-long-enough-password"} {
		t.Run("password "+password, func(t *testing.T) {
			_, err := f.accounts.Login(f.ctx, LoginRequest{
				Email: "sso@example.com", Password: password, IP: "203.0.113.9",
			})
			if err == nil {
				t.Fatalf("an account with no password was signed into with %q", password)
			}
			if !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Errorf("the refusal is %v, want invalid credentials - naming "+
					"single sign-on as the reason would tell an attacker which "+
					"accounts to attack how", err)
			}
		})
	}

	// The guard is pinned by the reason it records, not only by the refusal:
	// VerifyPassword also refuses an empty hash, so removing the explicit check
	// would leave the outcome the same and the trail silent about why. The
	// reason is what tells an administrator that somebody is trying a password
	// on an account that has none.
	users, err := f.accounts.Users(f.ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	var ssoUser domain.User
	for _, user := range users {
		if user.Email == "sso@example.com" {
			ssoUser = user
		}
	}
	if ssoUser.ID == 0 {
		t.Fatal("the provisioned account is not in the listing")
	}

	trail, err := f.svc.AuditTrail(f.ctx, "user", ssoUser.ID)
	if err != nil {
		t.Fatalf("audit trail: %v", err)
	}
	var reasons []string
	for _, event := range trail {
		if strings.Contains(event.Action, "login_failed") {
			reasons = append(reasons, event.Detail)
		}
	}
	if len(reasons) == 0 {
		t.Fatal("the attempts against a passwordless account were not recorded")
	}
	named := false
	for _, reason := range reasons {
		if strings.Contains(reason, "no password") ||
			strings.Contains(reason, "identity provider") {
			named = true
		}
	}
	if !named {
		t.Errorf("the recorded reasons are %v; none says the account has no "+
			"password, so the trail does not distinguish this from a wrong one",
			reasons)
	}
	if err := f.accounts.SetPassword(f.ctx, ssoUser.ID, "", "a-long-enough-password"); err != nil {
		t.Fatalf("set a password: %v", err)
	}
	if _, err := f.accounts.Login(f.ctx, LoginRequest{
		Email: "sso@example.com", Password: "a-long-enough-password", IP: "203.0.113.9",
	}); err != nil {
		t.Errorf("the account cannot sign in with the password just set: %v", err)
	}
}

// TestAnUnknownAddressCostsTheSameAsAWrongPassword.
//
// The messages are identical, and so is the work: an unknown account hashes a
// dummy password anyway. Without that, "no such account" returns in
// microseconds while a real verification takes the Argon2 cost, and the
// difference is measurable over a network - the endpoint would disclose which
// addresses have accounts despite saying the same thing to both.
//
// Timing is not asserted here, because a timing assertion in a unit test is a
// flake. What is asserted is that both paths refuse identically, and the
// dummy verification is covered by taking the unknown-account path at all.
func TestAnUnknownAddressCostsTheSameAsAWrongPassword(t *testing.T) {
	f := newServerFixture(t)

	unknown, err1 := f.accounts.Login(f.ctx, LoginRequest{
		Email: "nobody@example.com", Password: "a-long-enough-password", IP: "203.0.113.1",
	})
	wrong, err2 := f.accounts.Login(f.ctx, LoginRequest{
		Email: "admin@example.com", Password: "not-the-password", IP: "203.0.113.2",
	})

	if err1 == nil || err2 == nil {
		t.Fatalf("a login succeeded that should not have: %v, %v", err1, err2)
	}
	if err1.Error() != err2.Error() {
		t.Errorf("an unknown address is refused with %q and a wrong password with "+
			"%q; the difference says which addresses have accounts", err1, err2)
	}
	if unknown.CookieValue != "" || wrong.CookieValue != "" {
		t.Error("a failed login handed out a cookie")
	}

	// Both were recorded, so somebody reading the log can see an attack.
	trail, err := f.svc.AuditTrail(f.ctx, "user", f.admin.ID)
	if err != nil {
		t.Fatalf("audit trail: %v", err)
	}
	failures := 0
	for _, event := range trail {
		if strings.Contains(event.Action, "login_failed") {
			failures++
		}
	}
	if failures == 0 {
		t.Error("a failed login against a real account was not recorded")
	}
}

// TestASessionIsTouchedAsItIsUsed.
//
// The last-seen time is what the sessions screen shows, and it is written at
// most once a minute: on a busy screen every request would otherwise be a
// write to the one write connection.
func TestASessionIsTouchedAsItIsUsed(t *testing.T) {
	f := newServerFixture(t)

	login, err := f.accounts.Login(f.ctx, LoginRequest{
		Email: "admin@example.com", Password: "a-long-enough-password", IP: "203.0.113.9",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	ctx := f.asUser(f.admin)
	sessions, err := f.accounts.Sessions(ctx)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("%d sessions: %v", len(sessions), err)
	}
	first := sessions[0].LastSeenAt

	// A second request in the same minute does not write.
	f.now = f.now.Add(10 * time.Second)
	if _, _, err := f.accounts.ResolveSession(ctx, login.CookieValue); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	sessions, err = f.accounts.Sessions(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !sessions[0].LastSeenAt.Equal(first) {
		t.Errorf("last-seen moved from %s to %s after ten seconds; every request "+
			"on a busy screen would be a write", first, sessions[0].LastSeenAt)
	}

	// A minute later it does.
	f.now = f.now.Add(2 * time.Minute)
	if _, _, err := f.accounts.ResolveSession(ctx, login.CookieValue); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	sessions, err = f.accounts.Sessions(ctx)
	if err != nil {
		t.Fatalf("re-list: %v", err)
	}
	if !sessions[0].LastSeenAt.After(first) {
		t.Errorf("last-seen is still %s after two minutes of use", sessions[0].LastSeenAt)
	}
}

// TestDeactivatingAnAccountEndsItsOpenSessionsImmediately.
//
// Deactivating is what an administrator does the moment somebody should no
// longer have access. If an open session kept working until it expired, that
// action would do nothing for hours - and the person doing it would have no
// way to tell.
func TestDeactivatingAnAccountEndsItsOpenSessionsImmediately(t *testing.T) {
	f := newServerFixture(t)

	leaver, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Leaver", Email: "leaver@example.com",
		Password: "a-long-enough-password", Role: domain.RoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	login, err := f.accounts.Login(f.ctx, LoginRequest{
		Email: "leaver@example.com", Password: "a-long-enough-password", IP: "203.0.113.9",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, _, err := f.accounts.ResolveSession(f.ctx, login.CookieValue); err != nil {
		t.Fatalf("the session does not resolve while active: %v", err)
	}

	leaver.Active = false
	if err := f.accounts.UpdateUser(f.ctx, leaver); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	if _, _, err := f.accounts.ResolveSession(f.ctx, login.CookieValue); err == nil {
		t.Fatal("a deactivated account's open session still resolves")
	}

	// And the rows are gone, not merely refused: reactivating must not bring
	// the old sessions back with it.
	leaver.Active = true
	if err := f.accounts.UpdateUser(f.ctx, leaver); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if _, _, err := f.accounts.ResolveSession(f.ctx, login.CookieValue); err == nil {
		t.Error("reactivating an account brought its old sessions back")
	}
}

// TestListingAProjectsMembers.
//
// Who is on an engagement, which is both a screen and the thing every scope is
// computed from. Somebody not on the project may not read it: the membership
// list is who works for this customer.
func TestListingAProjectsMembers(t *testing.T) {
	f := newServerFixture(t)
	assignment, colleague := f.team(t)

	project, err := f.svc.Project(f.ctx, assignment.ProjectID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	members, err := f.accounts.Members(f.ctx, project.ID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("%d members, want the administrator and the colleague", len(members))
	}
	named := map[string]bool{}
	for _, member := range members {
		named[member.UserName] = true
		if member.ProjectName == "" {
			t.Error("a membership row does not say which project it is for")
		}
	}
	if !named["Colleague"] {
		t.Errorf("the colleague is not in %v", named)
	}

	// Removing them takes them out of the listing, and their scope with it.
	if err := f.accounts.RemoveMember(f.ctx, project.ID, colleague.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	after, err := f.accounts.Members(f.ctx, project.ID)
	if err != nil {
		t.Fatalf("re-list: %v", err)
	}
	if len(after) != 1 {
		t.Errorf("%d members after removing one, want 1", len(after))
	}

	// Removing somebody who is not a member is a stale button rather than an
	// error.
	if err := f.accounts.RemoveMember(f.ctx, project.ID, colleague.ID); err != nil {
		t.Errorf("removing a non-member = %v, want a no-op", err)
	}

	// And a member of no projects cannot read who is on one.
	stranger, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Stranger", Email: "stranger@example.com",
		Password: "a-long-enough-password", Role: domain.RoleMember,
	})
	if err != nil {
		t.Fatalf("create a stranger: %v", err)
	}
	if listed, err := f.accounts.Members(f.asUser(stranger), project.ID); err == nil && len(listed) > 0 {
		t.Errorf("somebody on no projects read %d membership rows, which is who "+
			"works for this customer", len(listed))
	}
}

// TestAnAdministratorNeverRemovesTheirOwnAccess.
//
// Removing the last administrator role leaves an instance nobody can
// administer, fixable only by editing the database. The rule is deliberately
// blunter than that: an administrator may never remove their own access, even
// with others in place, and is told to ask one of them.
//
// Blunter is better here. Counting the remaining administrators would have to
// be right under concurrency, and two administrators stepping down at the same
// moment would each see the other and both succeed. "Ask somebody else" cannot
// race.
func TestAnAdministratorNeverRemovesTheirOwnAccess(t *testing.T) {
	f := newServerFixture(t)

	demoted := f.admin
	demoted.Role = domain.RoleMember
	err := f.accounts.UpdateUser(f.ctx, demoted)
	if err == nil {
		t.Fatal("the only administrator demoted themselves")
	}
	if !strings.Contains(err.Error(), "another administrator") {
		t.Errorf("the refusal reads %q, which does not say what to do instead", err)
	}

	// Deactivating themselves is the same move by another route.
	disabled := f.admin
	disabled.Active = false
	if err := f.accounts.UpdateUser(f.ctx, disabled); err == nil {
		if _, loginErr := f.accounts.Login(f.ctx, LoginRequest{
			Email: "admin@example.com", Password: "a-long-enough-password", IP: "203.0.113.9",
		}); loginErr != nil {
			t.Error("the only administrator deactivated themselves and can no " +
				"longer sign in, leaving an instance nobody can administer")
		}
	}

	// Still refused with a second administrator in place: the rule is about who
	// is asking, not how many are left.
	second, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Second Admin", Email: "second@example.com",
		Password: "a-long-enough-password", Role: domain.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("create a second administrator: %v", err)
	}
	if err := f.accounts.UpdateUser(f.ctx, demoted); err == nil {
		t.Error("an administrator stepped down once another existed; the rule is " +
			"meant to be about who is asking rather than how many are left")
	}

	// And the other administrator can do it for them, which is what the
	// refusal tells them to arrange.
	if err := f.accounts.UpdateUser(f.asUser(second), demoted); err != nil {
		t.Errorf("another administrator could not demote the first: %v", err)
	}
}
