package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// serverFixture is a service wired the way server mode wires it: the RBAC
// authoriser with a real membership lookup, and the account service on top.
type serverFixture struct {
	svc      *Service
	accounts *Accounts
	db       *store.DB
	now      time.Time
	ctx      context.Context // an administrator's context
	admin    domain.User
}

func newServerFixture(t *testing.T) *serverFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("account tests touch the filesystem; skipped under -short")
	}

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	f := &serverFixture{db: db, now: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return f.now }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	f.svc = New(db, auth.RoleAuthorizer{IsProjectMember: db.IsProjectMember}, logger, clock)
	f.accounts = NewAccounts(db, f.svc, clock)

	admin, err := f.accounts.BootstrapFirstAdmin(ctx, NewUserInput{
		DisplayName: "Admin", Email: "admin@example.com", Password: "a-long-enough-password",
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	f.admin = admin
	f.ctx = auth.WithUser(ctx, admin)
	return f
}

// asUser returns a context acting as the given user.
func (f *serverFixture) asUser(u domain.User) context.Context {
	return auth.WithUser(context.Background(), u)
}

// TestBootstrapOnlyOnce: the one account-creation path that runs without an
// identity must refuse once the instance has accounts, or it is a way to mint an
// administrator at any time.
func TestBootstrapOnlyOnce(t *testing.T) {
	f := newServerFixture(t)

	_, err := f.accounts.BootstrapFirstAdmin(context.Background(), NewUserInput{
		DisplayName: "Intruder", Email: "intruder@example.com", Password: "another-long-password",
	})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("a second bootstrap should be refused, got %v", err)
	}
}

// TestLoginAndSession walks a complete sign-in.
func TestLoginAndSession(t *testing.T) {
	f := newServerFixture(t)

	result, err := f.accounts.Login(context.Background(), LoginRequest{
		Email: "admin@example.com", Password: "a-long-enough-password", IP: "203.0.113.1",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if result.CookieValue == "" || result.CSRFToken == "" {
		t.Fatal("login produced no session material")
	}

	user, session, err := f.accounts.ResolveSession(context.Background(), result.CookieValue)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	if user.ID != f.admin.ID {
		t.Errorf("session resolved to the wrong user: %d", user.ID)
	}
	if session.CSRFToken != result.CSRFToken {
		t.Error("the session's CSRF token does not match the one issued at login")
	}

	// Signing out revokes server-side, so the cookie stops working immediately
	// rather than merely being forgotten by the browser.
	if err := f.accounts.Logout(context.Background(), result.CookieValue); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, _, err := f.accounts.ResolveSession(context.Background(), result.CookieValue); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("a revoked session still resolved: %v", err)
	}
}

// TestLoginFailuresAreIndistinguishable: an unknown account and a wrong password
// must produce the same error, or the login form enumerates accounts.
func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	f := newServerFixture(t)

	_, unknownErr := f.accounts.Login(context.Background(), LoginRequest{
		Email: "nobody@example.com", Password: "some-long-password", IP: "203.0.113.2",
	})
	_, wrongErr := f.accounts.Login(context.Background(), LoginRequest{
		Email: "admin@example.com", Password: "the-wrong-password", IP: "203.0.113.3",
	})

	if !errors.Is(unknownErr, auth.ErrInvalidCredentials) {
		t.Errorf("unknown account: got %v", unknownErr)
	}
	if !errors.Is(wrongErr, auth.ErrInvalidCredentials) {
		t.Errorf("wrong password: got %v", wrongErr)
	}
	if unknownErr.Error() != wrongErr.Error() {
		t.Errorf("the two failures are distinguishable:\n  unknown: %v\n  wrong:   %v",
			unknownErr, wrongErr)
	}
}

// TestDisabledAccountCannotSignIn, and an account disabled mid-session loses
// access on its next request.
func TestDisabledAccountLosesAccess(t *testing.T) {
	f := newServerFixture(t)

	member, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Member", Email: "member@example.com",
		Password: "a-long-enough-password", Role: domain.RoleMember,
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}

	result, err := f.accounts.Login(context.Background(), LoginRequest{
		Email: "member@example.com", Password: "a-long-enough-password", IP: "203.0.113.4",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	member.Active = false
	if err := f.accounts.UpdateUser(f.ctx, member); err != nil {
		t.Fatalf("disable: %v", err)
	}

	// Disabling revokes the sessions outright; even if one survived, resolving
	// it checks the active flag.
	if _, _, err := f.accounts.ResolveSession(context.Background(), result.CookieValue); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("a disabled user's session still resolved: %v", err)
	}
	if _, err := f.accounts.Login(context.Background(), LoginRequest{
		Email: "member@example.com", Password: "a-long-enough-password", IP: "203.0.113.5",
	}); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("a disabled account signed in: %v", err)
	}
}

// TestRoleChangeRevokesSessions: a demotion that leaves old sessions running has
// demoted nobody.
func TestRoleChangeRevokesSessions(t *testing.T) {
	f := newServerFixture(t)

	user, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Temp Manager", Email: "manager@example.com",
		Password: "a-long-enough-password", Role: domain.RoleManager,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	result, err := f.accounts.Login(context.Background(), LoginRequest{
		Email: "manager@example.com", Password: "a-long-enough-password", IP: "203.0.113.6",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	user.Role = domain.RoleMember
	user.Active = true
	if err := f.accounts.UpdateUser(f.ctx, user); err != nil {
		t.Fatalf("demote: %v", err)
	}

	if _, _, err := f.accounts.ResolveSession(context.Background(), result.CookieValue); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Error("a session survived a role change")
	}
}

// TestAdminCannotLockThemselvesOut.
func TestAdminCannotRemoveOwnAdminRole(t *testing.T) {
	f := newServerFixture(t)

	self := f.admin
	self.Role = domain.RoleMember
	self.Active = true
	if err := f.accounts.UpdateUser(f.ctx, self); !errors.Is(err, ErrValidation) {
		t.Errorf("an administrator demoting themselves should be refused, got %v", err)
	}
}

// TestPasswordChangeRequiresTheCurrentOne, for your own account.
func TestPasswordChangeRequiresCurrentPassword(t *testing.T) {
	f := newServerFixture(t)

	err := f.accounts.SetPassword(f.ctx, f.admin.ID, "not-the-right-one", "a-brand-new-password")
	if !errors.Is(err, ErrValidation) {
		t.Errorf("changing your own password without the current one should fail, got %v", err)
	}

	if err := f.accounts.SetPassword(f.ctx, f.admin.ID,
		"a-long-enough-password", "a-brand-new-password"); err != nil {
		t.Fatalf("legitimate password change: %v", err)
	}
	if _, err := f.accounts.Login(context.Background(), LoginRequest{
		Email: "admin@example.com", Password: "a-brand-new-password", IP: "203.0.113.7",
	}); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
}

// TestNonAdminCannotManageUsers is the enforcement side of the role matrix,
// checked through the service rather than the authoriser alone.
func TestNonAdminCannotManageUsers(t *testing.T) {
	f := newServerFixture(t)

	member, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Member", Email: "member2@example.com",
		Password: "a-long-enough-password", Role: domain.RoleMember,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	memberCtx := f.asUser(member)

	if _, err := f.accounts.Users(memberCtx); !errors.Is(err, ErrForbidden) {
		t.Errorf("a member listed users: %v", err)
	}
	if _, err := f.accounts.CreateUser(memberCtx, NewUserInput{
		DisplayName: "Sneaky", Email: "sneaky@example.com",
		Password: "a-long-enough-password", Role: domain.RoleAdmin,
	}); !errors.Is(err, ErrForbidden) {
		t.Errorf("a member created an administrator: %v", err)
	}
}

// TestScopingHidesOtherProjects is the practical form of ASR-005: a member who
// is not attached to a project must not see it, its assignments, or the time
// recorded against it.
func TestScopingHidesOtherProjects(t *testing.T) {
	f := newServerFixture(t)

	// Two customers, each with a project.
	visibleCustomer, err := f.svc.CreateCustomer(f.ctx, domain.Customer{Name: "Visible", Currency: "EUR"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	hiddenCustomer, err := f.svc.CreateCustomer(f.ctx, domain.Customer{Name: "Hidden", Currency: "EUR"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	visibleProject, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: visibleCustomer.ID, Name: "Visible work", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	hiddenProject, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: hiddenCustomer.ID, Name: "Hidden work", BillableDefault: true,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := f.svc.CreateAssignment(f.ctx, domain.Assignment{
		ProjectID: hiddenProject.ID, Name: "Secret task", BillableDefault: true,
	}); err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	member, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Member", Email: "scoped@example.com",
		Password: "a-long-enough-password", Role: domain.RoleMember,
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	if err := f.accounts.AddMember(f.ctx, Membership{
		ProjectID: visibleProject.ID, UserID: member.ID,
	}); err != nil {
		t.Fatalf("add membership: %v", err)
	}

	memberCtx := f.asUser(member)

	projects, err := f.svc.Projects(memberCtx, 0, false)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != visibleProject.ID {
		t.Errorf("a member saw projects outside their membership: %+v", projects)
	}

	customers, err := f.svc.Customers(memberCtx, false)
	if err != nil {
		t.Fatalf("list customers: %v", err)
	}
	if len(customers) != 1 || customers[0].ID != visibleCustomer.ID {
		t.Errorf("a member saw customers outside their membership: %+v", customers)
	}

	assignments, err := f.svc.Assignments(memberCtx, 0, false)
	if err != nil {
		t.Fatalf("list assignments: %v", err)
	}
	for _, a := range assignments {
		if a.ProjectID == hiddenProject.ID {
			t.Errorf("a member saw an assignment in a project they do not belong to: %+v", a)
		}
	}
}

// TestUserWithNoMembershipsSeesNothing: an empty scope must mean "nothing", not
// "no restriction". This is the direction the failure has to go.
func TestUserWithNoMembershipsSeesNothing(t *testing.T) {
	f := newServerFixture(t)

	customer, err := f.svc.CreateCustomer(f.ctx, domain.Customer{Name: "Acme", Currency: "EUR"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	if _, err := f.svc.CreateProject(f.ctx, domain.Project{
		CustomerID: customer.ID, Name: "Work", BillableDefault: true,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	member, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Newcomer", Email: "newcomer@example.com",
		Password: "a-long-enough-password", Role: domain.RoleMember,
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}

	projects, err := f.svc.Projects(f.asUser(member), 0, false)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("a user with no memberships saw %d projects", len(projects))
	}
}

// TestClientIsScopedToItsCustomer.
func TestClientIsScopedToItsCustomer(t *testing.T) {
	f := newServerFixture(t)

	theirs, err := f.svc.CreateCustomer(f.ctx, domain.Customer{Name: "Their Company", Currency: "EUR"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	if _, err := f.svc.CreateCustomer(f.ctx, domain.Customer{Name: "Someone Else", Currency: "EUR"}); err != nil {
		t.Fatalf("create customer: %v", err)
	}

	client, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Client Contact", Email: "client@example.com",
		Password: "a-long-enough-password", Role: domain.RoleClient,
		ClientCustomerID: theirs.ID,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	customers, err := f.svc.Customers(f.asUser(client), false)
	if err != nil {
		t.Fatalf("list customers: %v", err)
	}
	if len(customers) != 1 || customers[0].ID != theirs.ID {
		t.Errorf("a client saw customers other than its own: %+v", customers)
	}
}

// TestClientRoleRequiresACustomer: a client account with no customer would be a
// read-only login with no boundary, which is the thing the role exists to impose.
func TestClientRoleRequiresACustomer(t *testing.T) {
	f := newServerFixture(t)

	_, err := f.accounts.CreateUser(f.ctx, NewUserInput{
		DisplayName: "Unbounded", Email: "unbounded@example.com",
		Password: "a-long-enough-password", Role: domain.RoleClient,
	})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("a client without a customer should be refused, got %v", err)
	}
}

// TestAuthenticationEventsAreAudited: a run of failures against one account is
// exactly what an operator needs to see, and it mutates nothing.
func TestAuthenticationEventsAreAudited(t *testing.T) {
	f := newServerFixture(t)

	if _, err := f.accounts.Login(context.Background(), LoginRequest{
		Email: "admin@example.com", Password: "a-long-enough-password", IP: "203.0.113.8",
	}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := f.accounts.Login(context.Background(), LoginRequest{
		Email: "admin@example.com", Password: "wrong", IP: "203.0.113.9",
	}); err == nil {
		t.Fatal("the wrong password should not succeed")
	}

	events, err := f.db.ListAuditEvents(context.Background(), "user", f.admin.ID, 50)
	if err != nil {
		t.Fatalf("audit trail: %v", err)
	}

	actions := map[string]bool{}
	for _, e := range events {
		actions[e.Action] = true
	}
	for _, want := range []string{"auth.login", "auth.login_failed"} {
		if !actions[want] {
			t.Errorf("no audit event for %s (have %v)", want, actions)
		}
	}
}

// TestPasswordsNeverAppearInAudit: the trail records that a password changed,
// never what it changed to.
func TestPasswordsNeverAppearInAudit(t *testing.T) {
	f := newServerFixture(t)
	const secret = "a-very-distinctive-password"

	if err := f.accounts.SetPassword(f.ctx, f.admin.ID, "a-long-enough-password", secret); err != nil {
		t.Fatalf("set password: %v", err)
	}

	events, err := f.db.ListAuditEvents(context.Background(), "user", f.admin.ID, 50)
	if err != nil {
		t.Fatalf("audit trail: %v", err)
	}
	for _, e := range events {
		if contains(e.Detail, secret) {
			t.Fatalf("a password reached the audit trail: %q", e.Detail)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
