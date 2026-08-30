package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Accounts and identities.
//
// Two things live here that are easy to get subtly wrong and impossible to
// notice afterwards: which column an identity is looked up by, and which columns
// a read actually selects. The second one has already cost this project a whole
// role - client_customer_id was missing from the identity query, so every client
// was scoped to customer zero and refused every screen after signing in
// successfully. Nothing failed; the role simply did not work.

// TestTheIdentityQueriesSelectTheWholeIdentity.
//
// The regression that motivated the rest of this file. A user's client customer
// is part of who they are, not a detail of the administration screen: the
// authoriser reads it on every request to decide what a client may see, and a
// zero there means "scoped to no customer", which is refused.
//
// Asserted for each of the three ways an identity is loaded, because they are
// three separate SELECT lists and the bug was one of them missing a column.
func TestTheIdentityQueriesSelectTheWholeIdentity(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	customer, err := db.CreateCustomer(ctx, domain.Customer{Name: "Acme", Currency: "SEK"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	created, err := db.CreateAccount(ctx, Account{
		User: domain.User{
			DisplayName: "The Client", Email: "client@example.com",
			Role: domain.RoleClient, TimeZone: "UTC", Theme: "light", Active: true,
			ClientCustomerID: customer.ID,
		},
		PasswordHash: "$argon2id$not-a-real-hash",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	byID, err := db.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if byID.ClientCustomerID != customer.ID {
		t.Errorf("GetUser lost the client's customer: %d, want %d. Every screen "+
			"refuses a client scoped to customer zero.",
			byID.ClientCustomerID, customer.ID)
	}

	account, err := db.AccountByEmail(ctx, "client@example.com")
	if err != nil {
		t.Fatalf("account by email: %v", err)
	}
	if account.User.ClientCustomerID != customer.ID {
		t.Errorf("AccountByEmail lost the client's customer: %d",
			account.User.ClientCustomerID)
	}

	first, err := db.FirstUser(ctx)
	if err != nil {
		t.Fatalf("first user: %v", err)
	}
	if first.ID != created.ID {
		t.Fatalf("FirstUser returned %d, want the only user %d", first.ID, created.ID)
	}
	if first.ClientCustomerID != customer.ID {
		t.Errorf("FirstUser lost the client's customer: %d", first.ClientCustomerID)
	}

	users, err := db.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 1 || users[0].ClientCustomerID != customer.ID {
		t.Errorf("ListUsers lost the client's customer: %+v", users)
	}
}

// TestEmailLoginIsCaseInsensitive.
//
// Nobody remembers how they capitalised their address when the account was made,
// and an address that fails to match is indistinguishable from a wrong password
// to the person typing it.
func TestEmailLoginIsCaseInsensitive(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if _, err := db.CreateAccount(ctx, Account{
		User: domain.User{
			DisplayName: "Someone", Email: "Someone@Example.COM",
			Role: domain.RoleMember, TimeZone: "UTC", Theme: "light", Active: true,
		},
		PasswordHash: "hash",
	}); err != nil {
		t.Fatalf("create account: %v", err)
	}

	for _, spelling := range []string{
		"someone@example.com", "SOMEONE@EXAMPLE.COM", "Someone@Example.com",
		"  someone@example.com  ", // what a paste from an email client looks like
	} {
		if _, err := db.AccountByEmail(ctx, spelling); err != nil {
			t.Errorf("%q did not find the account: %v", spelling, err)
		}
	}
	if _, err := db.AccountByEmail(ctx, "someone.else@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a different address matched: %v", err)
	}
}

// TestSSOIdentitiesAreLookedUpBySubject.
//
// An email address can be reassigned to a new person in a directory. Matching on
// it would hand them the previous holder's timesheet, which is why the link is
// the provider's immutable subject and why this asserts that changing the
// address does not change who is found.
func TestSSOIdentitiesAreLookedUpBySubject(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	user, err := db.CreateAccount(ctx, Account{
		User: domain.User{
			DisplayName: "Someone", Email: "someone@example.com",
			Role: domain.RoleMember, TimeZone: "UTC", Theme: "light", Active: true,
		},
		OIDCIssuer:  "https://login.example.com",
		OIDCSubject: "subject-0001",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	found, err := db.AccountByOIDCSubject(ctx, "https://login.example.com", "subject-0001")
	if err != nil {
		t.Fatalf("by subject: %v", err)
	}
	if found.User.ID != user.ID {
		t.Errorf("found user %d, want %d", found.User.ID, user.ID)
	}

	// The same subject at a different issuer is a different person. Two
	// directories can both mint "subject-0001".
	if _, err := db.AccountByOIDCSubject(ctx, "https://other.example.com", "subject-0001"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a subject from another issuer matched: %v", err)
	}

	// Their address changes, as addresses do. They are still the same person.
	user.Email = "someone.new@example.com"
	if err := db.UpdateUserAdmin(ctx, user); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, err := db.AccountByOIDCSubject(ctx, "https://login.example.com", "subject-0001")
	if err != nil {
		t.Fatalf("by subject after the address changed: %v", err)
	}
	if again.User.ID != user.ID {
		t.Error("changing an address changed who the provider's subject resolves to")
	}
}

// TestLinkingAnSSOIdentityToALocalAccount.
//
// What happens the first time somebody with a password account signs in through
// the provider. The account must be the same one, with its history, rather than
// a second account with the same name.
func TestLinkingAnSSOIdentityToALocalAccount(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	user, err := db.CreateAccount(ctx, Account{
		User: domain.User{
			DisplayName: "Someone", Email: "someone@example.com",
			Role: domain.RoleMember, TimeZone: "UTC", Theme: "light", Active: true,
		},
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	if err := db.LinkOIDCSubject(ctx, user.ID, "https://login.example.com", "subject-9"); err != nil {
		t.Fatalf("link: %v", err)
	}
	linked, err := db.AccountByOIDCSubject(ctx, "https://login.example.com", "subject-9")
	if err != nil {
		t.Fatalf("by subject: %v", err)
	}
	if linked.User.ID != user.ID {
		t.Errorf("linking created a second identity: %d and %d", linked.User.ID, user.ID)
	}
	// The password still works: linking adds a way in, it does not replace one.
	if linked.PasswordHash != "hash" {
		t.Errorf("linking an SSO identity cleared the password hash")
	}

	// Linking to a user who is not there must fail rather than silently do
	// nothing, or a bug upstream would look like a successful link.
	if err := db.LinkOIDCSubject(ctx, 9999, "https://login.example.com", "s"); err == nil {
		t.Error("linking an identity to a nonexistent user succeeded")
	}
}

// TestChangingAPasswordStampsWhenItChanged.
//
// The timestamp is what a policy about password age is computed from, and it is
// set in the same statement as the hash so the two cannot disagree.
func TestChangingAPasswordStampsWhenItChanged(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	user, err := db.CreateAccount(ctx, Account{
		User: domain.User{
			DisplayName: "Someone", Email: "someone@example.com",
			Role: domain.RoleMember, TimeZone: "UTC", Theme: "light", Active: true,
		},
		PasswordHash: "old-hash",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	if err := db.SetPasswordHash(ctx, user.ID, "new-hash"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	account, err := db.AccountByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("account by id: %v", err)
	}
	if account.PasswordHash != "new-hash" {
		t.Errorf("password hash = %q", account.PasswordHash)
	}
	if account.PasswordSetAt == "" {
		t.Error("changing a password recorded no timestamp")
	}

	if err := db.SetPasswordHash(ctx, 9999, "hash"); err == nil {
		t.Error("setting a password for a nonexistent user succeeded")
	}
}

// TestPreferencesAndAdministrativeFieldsAreSeparate.
//
// A user may change their own theme, zone and language; only an administrator
// may change their role, their address, whether they are active, and which
// customer a client belongs to. The two updates are separate statements, and
// each must leave the other's fields alone - a preferences save that reset a
// role would be a privilege change nobody made.
func TestPreferencesAndAdministrativeFieldsAreSeparate(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	customer, err := db.CreateCustomer(ctx, domain.Customer{Name: "Acme", Currency: "SEK"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	user, err := db.CreateAccount(ctx, Account{
		User: domain.User{
			DisplayName: "Someone", Email: "someone@example.com",
			Role: domain.RoleManager, TimeZone: "UTC", Theme: "light",
			Language: "en", Active: true, ClientCustomerID: customer.ID,
		},
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	if err := db.UpdateUserPreferences(ctx, user.ID, "dark", "Europe/Stockholm", "sv"); err != nil {
		t.Fatalf("update preferences: %v", err)
	}
	after, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if after.Theme != "dark" || after.TimeZone != "Europe/Stockholm" || after.Language != "sv" {
		t.Errorf("preferences were not saved: %+v", after)
	}
	if after.Role != domain.RoleManager {
		t.Errorf("saving preferences changed the role to %q", after.Role)
	}
	if after.ClientCustomerID != customer.ID {
		t.Errorf("saving preferences changed the client customer to %d",
			after.ClientCustomerID)
	}
	if !after.Active {
		t.Error("saving preferences deactivated the account")
	}

	after.Role = domain.RoleMember
	after.Active = false
	if err := db.UpdateUserAdmin(ctx, after); err != nil {
		t.Fatalf("update user: %v", err)
	}
	admin, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if admin.Role != domain.RoleMember || admin.Active {
		t.Errorf("the administrative update did not take: %+v", admin)
	}
	if admin.Theme != "dark" || admin.Language != "sv" {
		t.Errorf("the administrative update reset the user's own preferences: %+v", admin)
	}
}

// TestRecordingALoginDoesNotDisturbAnythingElse.
//
// It runs on every successful sign-in, which makes it the most frequently
// executed write in the application and the one where a stray column in the SET
// clause would do the most damage.
func TestRecordingALoginDoesNotDisturbAnythingElse(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	user, err := db.CreateAccount(ctx, Account{
		User: domain.User{
			DisplayName: "Someone", Email: "someone@example.com",
			Role: domain.RoleAdmin, TimeZone: "UTC", Theme: "light", Active: true,
		},
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	at := time.Date(2026, 6, 1, 8, 15, 0, 0, time.UTC)
	if err := db.RecordLogin(ctx, user.ID, at); err != nil {
		t.Fatalf("record login: %v", err)
	}

	account, err := db.AccountByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("account by id: %v", err)
	}
	if account.PasswordHash != "hash" || account.User.Role != domain.RoleAdmin {
		t.Errorf("recording a login changed the account: %+v", account.User)
	}

	users, err := db.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 1 || users[0].LastLoginAt.IsZero() {
		t.Errorf("the login was not recorded: %+v", users)
	}
	if !users[0].LastLoginAt.Equal(at) {
		t.Errorf("last login = %v, want %v", users[0].LastLoginAt, at)
	}
}

// TestUsersAreListedByName.
//
// The administration screen is a list somebody scans for a name, so the order is
// by name and case-insensitively - "de Vries" and "De Vries" belong next to each
// other rather than in two separate alphabets.
func TestUsersAreListedByName(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for _, name := range []string{"zoe", "Adam", "bo", "Åsa"} {
		if _, err := db.CreateUser(ctx, domain.User{
			DisplayName: name, Role: domain.RoleMember,
			TimeZone: "UTC", Theme: "light", Active: true,
		}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	users, err := db.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	var names []string
	for _, user := range users {
		names = append(names, user.DisplayName)
	}
	if got := strings.Join(names[:3], ","); got != "Adam,bo,zoe" {
		t.Errorf("users are not sorted case-insensitively by name: %v", names)
	}
}

// TestCountingUsersIsHowSetupKnowsItIsDone.
//
// An empty instance shows the setup screen, and it decides that by counting.
// Getting it wrong in either direction is bad in a specific way: too low leaves
// the setup screen open on a live instance, which is an account anybody can
// create.
func TestCountingUsersIsHowSetupKnowsItIsDone(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	count, err := db.CountUsers(ctx)
	if err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 0 {
		t.Errorf("a fresh database has %d users", count)
	}

	seedUser(t, db, "The First")
	if count, err = db.CountUsers(ctx); err != nil || count != 1 {
		t.Errorf("after one user: %d, %v", count, err)
	}
}
