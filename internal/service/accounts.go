package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Account and session handling for server mode.
//
// The rules that matter here, all from docs/adr/0006-authentication-model.md:
//
//   - Every authentication failure returns the same error, and takes about the
//     same time, so the login endpoint cannot be used to discover which accounts
//     exist.
//   - A session identifier is rotated on login and on any privilege change, so a
//     fixated session cannot survive either.
//   - A privilege change revokes the user's other sessions, because a change
//     that leaves old sessions alive has not taken effect.

// Membership is a user's attachment to a project.
//
// It is an alias rather than a copy so there is no mapping to keep in step, and
// it exists so that the HTTP layer can name the type without importing the store
// package - the layering rule in docs/adr/0012-layered-package-structure.md.
type Membership = store.ProjectMember

// Accounts provides authentication and user administration. It is a separate
// type from Service because its methods run *before* an identity exists, so most
// of them cannot follow the "authorise first" rule the rest of the service layer
// does - keeping them apart makes that difference visible rather than hidden
// among ordinary methods.
type Accounts struct {
	db      *store.DB
	limiter *auth.LoginLimiter
	now     Clock
	svc     *Service
}

// NewAccounts builds the account service.
func NewAccounts(db *store.DB, svc *Service, now Clock) *Accounts {
	if now == nil {
		now = time.Now
	}
	return &Accounts{db: db, limiter: auth.NewLoginLimiter(now), now: now, svc: svc}
}

// LoginRequest is one sign-in attempt.
type LoginRequest struct {
	Email     string
	Password  string
	IP        string
	UserAgent string
}

// LoginResult carries what the caller needs to set the session cookie.
type LoginResult struct {
	User domain.User
	// CookieValue is the only time the session identifier exists in plaintext.
	// It is handed to the browser and never stored.
	CookieValue string
	CSRFToken   string
	ExpiresAt   time.Time
}

// Login authenticates an email and password and starts a session.
func (a *Accounts) Login(ctx context.Context, req LoginRequest) (LoginResult, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))

	// Two keys: the account being targeted, and the address doing the targeting.
	// Limiting only by account leaves password spraying across many accounts
	// untouched; limiting only by address leaves a distributed attack on one
	// account untouched.
	accountKey := "account:" + email
	addressKey := "address:" + req.IP

	if allowed, wait := a.limiter.Allowed(accountKey, addressKey); !allowed {
		return LoginResult{}, fmt.Errorf("%w: too many attempts, try again in %s",
			auth.ErrInvalidCredentials, wait.Round(time.Second))
	}

	account, err := a.db.AccountByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Hash anyway. Without this, "unknown account" returns in
			// microseconds while a real verification takes the Argon2 cost, and
			// the difference is measurable over the network - the endpoint would
			// disclose which addresses have accounts despite the identical
			// message.
			auth.DummyVerify(req.Password)
			a.limiter.Failed(accountKey, addressKey)
			a.logAuthEvent(ctx, "auth.login_failed", 0, email, req.IP, "unknown account")
			return LoginResult{}, auth.ErrInvalidCredentials
		}
		return LoginResult{}, err
	}

	// An account with no password hash authenticates through SSO only. Falling
	// through to a hash comparison against an empty string would be a disaster,
	// so it is refused explicitly.
	if account.PasswordHash == "" {
		auth.DummyVerify(req.Password)
		a.limiter.Failed(accountKey, addressKey)
		a.logAuthEvent(ctx, "auth.login_failed", account.User.ID, email, req.IP,
			"account has no password; it signs in through the identity provider")
		return LoginResult{}, auth.ErrInvalidCredentials
	}

	if err := auth.VerifyPassword(req.Password, account.PasswordHash); err != nil {
		a.limiter.Failed(accountKey, addressKey)
		a.logAuthEvent(ctx, "auth.login_failed", account.User.ID, email, req.IP, "wrong password")
		return LoginResult{}, auth.ErrInvalidCredentials
	}

	// A disabled account is checked after the password, so that the response to
	// a wrong password and to a disabled account are indistinguishable.
	if !account.User.Active {
		a.limiter.Failed(accountKey, addressKey)
		a.logAuthEvent(ctx, "auth.login_denied", account.User.ID, email, req.IP, "account disabled")
		return LoginResult{}, auth.ErrInvalidCredentials
	}

	a.limiter.Succeeded(accountKey, addressKey)

	// Raise the hashing cost transparently if the defaults have moved on since
	// this password was set. Now is the only moment the plaintext exists.
	if auth.NeedsRehash(account.PasswordHash) {
		if hash, hashErr := auth.HashPassword(req.Password); hashErr == nil {
			_ = a.db.SetPasswordHash(ctx, account.User.ID, hash)
		}
	}

	return a.startSession(ctx, account.User, req.IP, req.UserAgent, "password")
}

// LoginWithOIDC completes a single sign-on flow, creating or linking an account.
func (a *Accounts) LoginWithOIDC(ctx context.Context, claims auth.Claims, mappedRole, ip, userAgent string) (LoginResult, error) {
	// Linked by the immutable subject, never by email.
	account, err := a.db.AccountByOIDCSubject(ctx, claims.Issuer, claims.Subject)
	switch {
	case err == nil:
		// Known identity.
	case errors.Is(err, store.ErrNotFound):
		if account, err = a.provisionOIDCUser(ctx, claims, mappedRole); err != nil {
			return LoginResult{}, err
		}
	default:
		return LoginResult{}, err
	}

	if !account.User.Active {
		a.logAuthEvent(ctx, "auth.login_denied", account.User.ID, account.User.Email, ip,
			"account disabled")
		return LoginResult{}, auth.ErrInvalidCredentials
	}

	return a.startSession(ctx, account.User, ip, userAgent, "oidc")
}

// provisionOIDCUser creates an account on first SSO sign-in, or links the
// provider identity to an existing local account.
func (a *Accounts) provisionOIDCUser(ctx context.Context, claims auth.Claims, mappedRole string) (store.Account, error) {
	email := strings.ToLower(strings.TrimSpace(claims.Email))

	// If a local account already uses this address, link rather than duplicate -
	// but only when the provider says the address is verified. An unverified
	// email claim is an assertion the user made about themselves, and honouring
	// it would let someone claim an existing colleague's account.
	if email != "" && claims.EmailVerified {
		existing, err := a.db.AccountByEmail(ctx, email)
		switch {
		case err == nil:
			if existing.OIDCSubject != "" && existing.OIDCSubject != claims.Subject {
				return store.Account{}, errors.New(
					"this email is already linked to a different identity-provider account")
			}
			if err := a.db.LinkOIDCSubject(ctx, existing.User.ID, claims.Issuer, claims.Subject); err != nil {
				return store.Account{}, err
			}
			a.logAuthEvent(ctx, "auth.oidc_linked", existing.User.ID, email, "", "linked by verified email")
			return a.db.AccountByID(ctx, existing.User.ID)
		case errors.Is(err, store.ErrNotFound):
			// Fall through to creation.
		default:
			return store.Account{}, err
		}
	}

	// Least privilege for a brand-new account: a member with no project
	// memberships sees nothing but their own empty timesheet until somebody
	// grants access. An unmapped or unrecognised role claim lands here too.
	role := domain.RoleMember
	if mapped := domain.Role(mappedRole); isKnownRole(mapped) {
		role = mapped
	}

	name := claims.Name
	if name == "" {
		name = email
	}
	if name == "" {
		name = "SSO user"
	}

	user, err := a.db.CreateAccount(ctx, store.Account{
		User: domain.User{
			DisplayName: name, Email: email, Role: role,
			TimeZone: "UTC", Theme: "light", Active: true,
		},
		OIDCIssuer:  claims.Issuer,
		OIDCSubject: claims.Subject,
	})
	if err != nil {
		return store.Account{}, err
	}
	a.logAuthEvent(ctx, "auth.oidc_provisioned", user.ID, email, "", "role "+string(role))
	return a.db.AccountByID(ctx, user.ID)
}

// startSession issues a new session for an authenticated user.
func (a *Accounts) startSession(ctx context.Context, user domain.User, ip, userAgent, method string) (LoginResult, error) {
	cookieValue, idHash, err := auth.NewSessionID()
	if err != nil {
		return LoginResult{}, err
	}
	csrfToken, err := auth.NewCSRFToken()
	if err != nil {
		return LoginResult{}, err
	}

	now := a.now()
	session := auth.Session{
		IDHash:     idHash,
		UserID:     user.ID,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(auth.SessionAbsoluteTimeout),
		IP:         ip,
		// Truncated: a user agent string is attacker-controlled and unbounded,
		// and it is only ever shown back to the user as a hint about where they
		// are signed in.
		UserAgent: truncate(userAgent, 255),
		CSRFToken: csrfToken,
	}
	if err := a.db.CreateSession(ctx, session); err != nil {
		return LoginResult{}, err
	}
	if err := a.db.RecordLogin(ctx, user.ID, now); err != nil {
		return LoginResult{}, err
	}

	a.logAuthEvent(ctx, "auth.login", user.ID, user.Email, ip, "method "+method)

	return LoginResult{
		User:        user,
		CookieValue: cookieValue,
		CSRFToken:   csrfToken,
		ExpiresAt:   session.ExpiresAt,
	}, nil
}

// ResolveSession turns a cookie value into the user it authenticates.
//
// Expiry is enforced here, on every request, rather than relying on the periodic
// sweep - so a missed sweep is a storage-growth problem and never a security
// one.
func (a *Accounts) ResolveSession(ctx context.Context, cookieValue string) (domain.User, auth.Session, error) {
	if cookieValue == "" {
		return domain.User{}, auth.Session{}, auth.ErrUnauthenticated
	}

	session, err := a.db.GetSession(ctx, auth.HashSessionID(cookieValue))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.User{}, auth.Session{}, auth.ErrUnauthenticated
		}
		return domain.User{}, auth.Session{}, err
	}

	now := a.now()
	if session.Expired(now) {
		// Delete rather than leave it to the sweep: an expired session should
		// stop being a row that anyone can look at.
		_ = a.db.DeleteSession(ctx, session.IDHash)
		return domain.User{}, auth.Session{}, auth.ErrUnauthenticated
	}

	user, err := a.db.GetUser(ctx, session.UserID)
	if err != nil {
		return domain.User{}, auth.Session{}, auth.ErrUnauthenticated
	}
	// An account disabled mid-session loses access on its next request, without
	// waiting for the session to expire.
	if !user.Active {
		_, _ = a.db.DeleteUserSessions(ctx, user.ID)
		return domain.User{}, auth.Session{}, auth.ErrUnauthenticated
	}

	// Touch at most once a minute. Writing on every request would serialise
	// every page load behind SQLite's single writer for no benefit.
	if now.Sub(session.LastSeenAt) > time.Minute {
		if err := a.db.TouchSession(ctx, session.IDHash, now); err != nil {
			return domain.User{}, auth.Session{}, err
		}
	}
	return user, session, nil
}

// Logout revokes one session.
func (a *Accounts) Logout(ctx context.Context, cookieValue string) error {
	if cookieValue == "" {
		return nil
	}
	return a.db.DeleteSession(ctx, auth.HashSessionID(cookieValue))
}

// Sessions lists the acting user's own active sessions.
func (a *Accounts) Sessions(ctx context.Context) ([]auth.Session, error) {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return nil, err
	}
	return a.db.ListUserSessions(ctx, actor.ID)
}

// RevokeOtherSessions signs the acting user out everywhere else - the "I left
// myself logged in somewhere" button.
func (a *Accounts) RevokeOtherSessions(ctx context.Context, keep auth.Session) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	sessions, err := a.db.ListUserSessions(ctx, actor.ID)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.IDHash == keep.IDHash {
			continue
		}
		if err := a.db.DeleteSession(ctx, s.IDHash); err != nil {
			return err
		}
	}
	return a.svc.recordAudit(ctx, "auth.revoke_sessions", "user", actor.ID, nil)
}

// PruneSessions removes expired sessions. Called periodically by the server.
func (a *Accounts) PruneSessions(ctx context.Context) (int64, error) {
	return a.db.PruneSessions(ctx, a.now())
}

// ------------------------------------------------------ user administration --

// NewUserInput describes an account an administrator is creating.
type NewUserInput struct {
	DisplayName string
	Email       string
	Password    string
	Role        domain.Role
	TimeZone    string
	// ClientCustomerID is required for the client role and ignored otherwise.
	ClientCustomerID int64
}

// CreateUser adds an account. Administrators only.
func (a *Accounts) CreateUser(ctx context.Context, in NewUserInput) (domain.User, error) {
	if err := a.svc.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "user"}); err != nil {
		return domain.User{}, err
	}
	return a.createUser(ctx, in, true)
}

// createUser is the shared implementation, also used to bootstrap the first
// administrator before any identity exists.
func (a *Accounts) createUser(ctx context.Context, in NewUserInput, audit bool) (domain.User, error) {
	if strings.TrimSpace(in.DisplayName) == "" {
		return domain.User{}, fmt.Errorf("%w: a name is required", ErrValidation)
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" {
		return domain.User{}, fmt.Errorf("%w: an email address is required", ErrValidation)
	}
	if !isKnownRole(in.Role) {
		return domain.User{}, fmt.Errorf("%w: unknown role %q", ErrValidation, in.Role)
	}
	// A client user who is not scoped to a customer would be a read-only account
	// with no boundary, which is exactly the thing the role exists to impose.
	if in.Role == domain.RoleClient && in.ClientCustomerID == 0 {
		return domain.User{}, fmt.Errorf("%w: a client user must be attached to a customer", ErrValidation)
	}

	hash := ""
	if in.Password != "" {
		var err error
		if hash, err = auth.HashPassword(in.Password); err != nil {
			return domain.User{}, fmt.Errorf("%w: %s", ErrValidation, err)
		}
	}

	timeZone := in.TimeZone
	if timeZone == "" {
		timeZone = "UTC"
	}

	user, err := a.db.CreateAccount(ctx, store.Account{
		User: domain.User{
			DisplayName: strings.TrimSpace(in.DisplayName), Email: email, Role: in.Role,
			TimeZone: timeZone, Theme: "light", Active: true,
			ClientCustomerID: in.ClientCustomerID,
		},
		PasswordHash: hash,
	})
	if err != nil {
		return domain.User{}, err
	}

	if audit {
		if err := a.svc.recordAudit(ctx, "user.create", "user", user.ID, map[string]any{
			"email": email, "role": string(in.Role),
		}); err != nil {
			return domain.User{}, err
		}
	}
	return user, nil
}

// BootstrapFirstAdmin creates the initial administrator on an empty instance.
//
// It refuses once any account exists, so it cannot be used later to mint an
// administrator without authorisation. This is the one account-creation path
// that runs without an identity, and the emptiness check is what keeps it safe.
func (a *Accounts) BootstrapFirstAdmin(ctx context.Context, in NewUserInput) (domain.User, error) {
	count, err := a.db.CountUsers(ctx)
	if err != nil {
		return domain.User{}, err
	}
	if count > 0 {
		return domain.User{}, fmt.Errorf("%w: this instance already has accounts", ErrForbidden)
	}
	in.Role = domain.RoleAdmin
	if in.Password == "" {
		return domain.User{}, fmt.Errorf("%w: the first administrator needs a password", ErrValidation)
	}
	return a.createUser(ctx, in, false)
}

// Users lists every account, for the administration screen.
func (a *Accounts) Users(ctx context.Context) ([]domain.User, error) {
	if err := a.svc.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "user"}); err != nil {
		return nil, err
	}
	return a.db.ListUsers(ctx)
}

// UpdateUser changes a user's name, email, role or active state.
//
// Any of those is a privilege change, so every session belonging to that user is
// revoked: a demotion that leaves the old sessions running has demoted nobody.
func (a *Accounts) UpdateUser(ctx context.Context, user domain.User) error {
	if err := a.svc.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "user", ID: user.ID}); err != nil {
		return notFoundFor(err)
	}
	if !isKnownRole(user.Role) {
		return fmt.Errorf("%w: unknown role %q", ErrValidation, user.Role)
	}

	before, err := a.db.GetUser(ctx, user.ID)
	if err != nil {
		return err
	}

	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	// An administrator removing their own administrator role, or disabling
	// themselves, can lock everyone out of the instance. Refuse rather than let
	// someone do it by accident.
	if actor.ID == user.ID && before.Role == domain.RoleAdmin &&
		(user.Role != domain.RoleAdmin || !user.Active) {
		return fmt.Errorf("%w: you cannot remove your own administrator access; "+
			"ask another administrator to do it", ErrValidation)
	}

	if err := a.db.UpdateUserAdmin(ctx, user); err != nil {
		return err
	}

	if before.Role != user.Role || before.Active != user.Active {
		if _, err := a.db.DeleteUserSessions(ctx, user.ID); err != nil {
			return err
		}
	}

	return a.svc.recordAudit(ctx, "user.update", "user", user.ID, map[string]any{
		"role":   map[string]any{"from": string(before.Role), "to": string(user.Role)},
		"active": map[string]any{"from": before.Active, "to": user.Active},
	})
}

// SetPassword sets a user's password.
//
// A user may change their own; an administrator may set anybody's. Either way
// every other session for that account is revoked, because a password change
// that leaves an attacker's session alive has achieved nothing.
func (a *Accounts) SetPassword(ctx context.Context, userID int64, currentPassword, newPassword string) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}

	if actor.ID != userID {
		if err := a.svc.authz.Can(ctx, auth.ActionManage, auth.Resource{Type: "user", ID: userID}); err != nil {
			return notFoundFor(err)
		}
	} else {
		// Changing your own password requires proving you know the current one,
		// so a borrowed unlocked browser cannot be used to take the account over.
		account, err := a.db.AccountByID(ctx, userID)
		if err != nil {
			return err
		}
		if account.PasswordHash != "" {
			if err := auth.VerifyPassword(currentPassword, account.PasswordHash); err != nil {
				return fmt.Errorf("%w: the current password is not correct", ErrValidation)
			}
		}
	}

	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrValidation, err)
	}
	if err := a.db.SetPasswordHash(ctx, userID, hash); err != nil {
		return err
	}
	if _, err := a.db.DeleteUserSessions(ctx, userID); err != nil {
		return err
	}
	return a.svc.recordAudit(ctx, "user.set_password", "user", userID, nil)
}

// ---------------------------------------------------------- memberships ----

// AddMember attaches a user to a project.
func (a *Accounts) AddMember(ctx context.Context, m store.ProjectMember) error {
	project, err := a.db.GetProject(ctx, m.ProjectID)
	if err != nil {
		return err
	}
	if err := a.svc.authz.Can(ctx, auth.ActionManage, auth.Resource{
		Type: "project_member", ProjectID: m.ProjectID, CustomerID: project.CustomerID,
	}); err != nil {
		return notFoundFor(err)
	}
	if err := a.db.AddProjectMember(ctx, m); err != nil {
		return err
	}
	return a.svc.recordAudit(ctx, "project_member.add", "project", m.ProjectID, map[string]any{
		"user_id": m.UserID, "rate_minor": m.RateMinor,
	})
}

// RemoveMember detaches a user from a project.
func (a *Accounts) RemoveMember(ctx context.Context, projectID, userID int64) error {
	project, err := a.db.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	if err := a.svc.authz.Can(ctx, auth.ActionManage, auth.Resource{
		Type: "project_member", ProjectID: projectID, CustomerID: project.CustomerID,
	}); err != nil {
		return notFoundFor(err)
	}
	if err := a.db.RemoveProjectMember(ctx, projectID, userID); err != nil {
		return err
	}
	return a.svc.recordAudit(ctx, "project_member.remove", "project", projectID, map[string]any{
		"user_id": userID,
	})
}

// Members lists project memberships.
func (a *Accounts) Members(ctx context.Context, projectID int64) ([]store.ProjectMember, error) {
	if err := a.svc.authz.Can(ctx, auth.ActionView, auth.Resource{
		Type: "project_member", ProjectID: projectID,
	}); err != nil {
		return nil, err
	}
	return a.db.ListProjectMembers(ctx, projectID)
}

// --------------------------------------------------------------- helpers ----

// logAuthEvent records an authentication event in the audit trail.
//
// Authentication events are logged whether or not they changed any data, and
// whether or not they succeeded: a run of failures against one account is
// exactly what an operator needs to see, and it mutates nothing.
//
// There is no actor in the context during a login, so this writes the audit row
// directly rather than through Service.audit, which requires one. The password
// itself is never a parameter here and never reaches the log.
func (a *Accounts) logAuthEvent(ctx context.Context, action string, userID int64, email, ip, detail string) {
	meta := requestMetaFrom(ctx)
	if ip == "" {
		ip = meta.IP
	}

	err := a.db.InTx(ctx, func(tx *sql.Tx) error {
		return store.InsertAuditTx(ctx, tx, store.AuditEvent{
			At: a.now(), ActorID: userID, ActorName: email,
			Action: action, ResourceType: "user", ResourceID: userID,
			Detail: fmt.Sprintf("%q", detail), IP: ip, RequestID: meta.RequestID,
		})
	})
	if err != nil && a.svc != nil && a.svc.log != nil {
		// A failure to write the audit row must not block a login response, but
		// it must be visible: an audit trail with silent holes is worse than
		// none.
		a.svc.log.ErrorContext(ctx, "could not record authentication event",
			"action", action, "error", err.Error())
	}
	if a.svc != nil && a.svc.log != nil {
		a.svc.log.InfoContext(ctx, "audit",
			"action", action, "resource_type", "user", "resource_id", userID,
			"actor", email, "ip", ip, "detail", detail)
	}
}

// isKnownRole guards against a role arriving from a form or a claim mapping that
// the authoriser would not recognise - which would otherwise fall through to its
// default-deny branch and produce a user who can do nothing, with no explanation.
func isKnownRole(role domain.Role) bool {
	switch role {
	case domain.RoleAdmin, domain.RoleManager, domain.RoleMember, domain.RoleClient:
		return true
	default:
		return false
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
