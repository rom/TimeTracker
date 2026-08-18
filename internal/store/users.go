package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Account queries: the credential columns, the user list, and project
// membership. The layer above decides who may call these; this file only knows
// how to read and write the rows.

// Account is a user together with the credential material that never leaves the
// server. It is a separate type from domain.User precisely so that a password
// hash cannot end up in a template or a JSON response by accident: the type the
// rest of the application handles simply has no field for it.
type Account struct {
	User          domain.User
	PasswordHash  string
	OIDCSubject   string
	OIDCIssuer    string
	TOTPSecret    string
	PasswordSetAt string
}

const userSelect = `
	SELECT id, display_name, email, role, time_zone, theme, language, active, created_at,
	       password_hash, oidc_subject, oidc_issuer, totp_secret, client_customer_id
	FROM users`

// AccountByEmail loads an account for a password login.
//
// Email comparison is case-insensitive, because people do not remember how they
// capitalised their address when the account was made.
func (db *DB) AccountByEmail(ctx context.Context, email string) (Account, error) {
	row := db.read.QueryRowContext(ctx,
		userSelect+` WHERE email = ? COLLATE NOCASE`, strings.TrimSpace(email))
	return scanAccount(row)
}

// AccountByOIDCSubject loads an account by the provider's immutable subject
// claim.
//
// Linking on the subject and never on the email address is deliberate: an email
// can be reassigned to a different person in a directory, and matching on it
// would hand them the previous holder's timesheet.
// See docs/adr/0006-authentication-model.md.
func (db *DB) AccountByOIDCSubject(ctx context.Context, issuer, subject string) (Account, error) {
	row := db.read.QueryRowContext(ctx,
		userSelect+` WHERE oidc_issuer = ? AND oidc_subject = ?`, issuer, subject)
	return scanAccount(row)
}

// AccountByID loads an account by user id.
func (db *DB) AccountByID(ctx context.Context, id int64) (Account, error) {
	row := db.read.QueryRowContext(ctx, userSelect+` WHERE id = ?`, id)
	return scanAccount(row)
}

func scanAccount(row rowScanner) (Account, error) {
	var a Account
	var role, createdAt string
	var active int
	err := row.Scan(&a.User.ID, &a.User.DisplayName, &a.User.Email, &role,
		&a.User.TimeZone, &a.User.Theme, &a.User.Language, &active, &createdAt,
		&a.PasswordHash, &a.OIDCSubject, &a.OIDCIssuer, &a.TOTPSecret, &a.User.ClientCustomerID)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	a.User.Role = domain.Role(role)
	a.User.Active = active != 0
	if a.User.CreatedAt, err = parseTime(createdAt); err != nil {
		return Account{}, err
	}
	return a, nil
}

// CreateAccount inserts a user together with their credentials.
func (db *DB) CreateAccount(ctx context.Context, a Account) (domain.User, error) {
	now := time.Now()
	res, err := db.write.ExecContext(ctx, `
		INSERT INTO users (display_name, email, role, time_zone, theme, language, active,
		                   created_at, password_hash, oidc_subject, oidc_issuer, totp_secret,
		                   password_set_at, client_customer_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.User.DisplayName, a.User.Email, string(a.User.Role), a.User.TimeZone, a.User.Theme,
		a.User.Language, boolToInt(a.User.Active), formatTime(now), a.PasswordHash,
		a.OIDCSubject, a.OIDCIssuer, a.TOTPSecret, a.PasswordSetAt, a.User.ClientCustomerID)
	if err != nil {
		return domain.User{}, fmt.Errorf("create account: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.User{}, err
	}
	a.User.ID = id
	a.User.CreatedAt = now
	return a.User, nil
}

// SetPasswordHash replaces a user's password hash.
//
// Callers revoke the user's other sessions afterwards: a password change that
// leaves an attacker's existing session alive has not achieved anything.
func (db *DB) SetPasswordHash(ctx context.Context, userID int64, hash string) error {
	res, err := db.write.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, password_set_at = ? WHERE id = ?`,
		hash, formatTime(time.Now()), userID)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	return requireOneRow(res)
}

// LinkOIDCSubject attaches a provider identity to an existing account, used when
// a local account signs in through SSO for the first time.
func (db *DB) LinkOIDCSubject(ctx context.Context, userID int64, issuer, subject string) error {
	res, err := db.write.ExecContext(ctx,
		`UPDATE users SET oidc_issuer = ?, oidc_subject = ? WHERE id = ?`, issuer, subject, userID)
	if err != nil {
		return fmt.Errorf("link OIDC subject: %w", err)
	}
	return requireOneRow(res)
}

// RecordLogin stamps a successful sign-in.
func (db *DB) RecordLogin(ctx context.Context, userID int64, at time.Time) error {
	_, err := db.write.ExecContext(ctx,
		`UPDATE users SET last_login_at = ? WHERE id = ?`, formatTime(at), userID)
	return err
}

// UpdateUserAdmin saves the fields only an administrator may change.
func (db *DB) UpdateUserAdmin(ctx context.Context, u domain.User) error {
	res, err := db.write.ExecContext(ctx, `
		UPDATE users SET display_name = ?, email = ?, role = ?, active = ?, client_customer_id = ?
		WHERE id = ?`,
		u.DisplayName, u.Email, string(u.Role), boolToInt(u.Active), u.ClientCustomerID, u.ID)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	return requireOneRow(res)
}

// ListUsers returns every user, for the administration screen.
func (db *DB) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := db.read.QueryContext(ctx, `
		SELECT id, display_name, email, role, time_zone, theme, language, active, created_at,
		       client_customer_id, oidc_subject, last_login_at
		FROM users ORDER BY display_name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var users []domain.User
	for rows.Next() {
		var u domain.User
		var role, createdAt, oidcSubject, lastLogin string
		var active int
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Email, &role, &u.TimeZone, &u.Theme,
			&u.Language, &active, &createdAt, &u.ClientCustomerID, &oidcSubject,
			&lastLogin); err != nil {
			return nil, err
		}
		u.Role = domain.Role(role)
		u.Active = active != 0
		u.UsesSSO = oidcSubject != ""
		var perr error
		if u.CreatedAt, perr = parseTime(createdAt); perr != nil {
			return nil, perr
		}
		if lastLogin != "" {
			if u.LastLoginAt, perr = parseTime(lastLogin); perr != nil {
				return nil, perr
			}
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// CountUsers reports how many accounts exist, used to decide whether an
// instance still needs its first administrator.
func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var count int
	err := db.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

// ------------------------------------------------------- project membership --

// ProjectMember is one user's attachment to one project.
type ProjectMember struct {
	ProjectID    int64
	UserID       int64
	UserName     string
	ProjectName  string
	CustomerName string
	RoleOverride string
	RateMinor    int64
}

// AddProjectMember attaches a user to a project, or updates the attachment.
func (db *DB) AddProjectMember(ctx context.Context, m ProjectMember) error {
	_, err := db.write.ExecContext(ctx, `
		INSERT INTO project_members (project_id, user_id, role_override, rate_minor, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (project_id, user_id)
		DO UPDATE SET role_override = excluded.role_override, rate_minor = excluded.rate_minor`,
		m.ProjectID, m.UserID, m.RoleOverride, m.RateMinor, formatTime(time.Now()))
	if err != nil {
		return fmt.Errorf("add project member: %w", err)
	}
	return nil
}

// RemoveProjectMember detaches a user from a project.
func (db *DB) RemoveProjectMember(ctx context.Context, projectID, userID int64) error {
	_, err := db.write.ExecContext(ctx,
		`DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, userID)
	return err
}

// IsProjectMember reports whether a user is attached to a project.
//
// This is the lookup the authoriser calls, so it is one indexed row read and
// nothing more.
func (db *DB) IsProjectMember(ctx context.Context, userID, projectID int64) (bool, error) {
	var exists int
	err := db.read.QueryRowContext(ctx,
		`SELECT 1 FROM project_members WHERE user_id = ? AND project_id = ?`,
		userID, projectID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check project membership: %w", err)
	}
	return true, nil
}

// ProjectIDsForUser returns the projects a user belongs to.
//
// Listing queries take this set rather than filtering afterwards: a query that
// returns everything and then hides rows in the template is one template bug
// away from a leak.
func (db *DB) ProjectIDsForUser(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := db.read.QueryContext(ctx,
		`SELECT project_id FROM project_members WHERE user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("list project memberships: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListProjectMembers returns the members of a project, or of every project when
// projectID is zero.
func (db *DB) ListProjectMembers(ctx context.Context, projectID int64) ([]ProjectMember, error) {
	query := `
		SELECT pm.project_id, pm.user_id, pm.role_override, pm.rate_minor,
		       u.display_name, p.name, c.name
		FROM project_members pm
		JOIN users     u ON u.id = pm.user_id
		JOIN projects  p ON p.id = pm.project_id
		JOIN customers c ON c.id = p.customer_id`
	var args []any
	if projectID != 0 {
		query += ` WHERE pm.project_id = ?`
		args = append(args, projectID)
	}
	query += ` ORDER BY c.name COLLATE NOCASE, p.name COLLATE NOCASE, u.display_name COLLATE NOCASE`

	rows, err := db.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list project members: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var members []ProjectMember
	for rows.Next() {
		var m ProjectMember
		if err := rows.Scan(&m.ProjectID, &m.UserID, &m.RoleOverride, &m.RateMinor,
			&m.UserName, &m.ProjectName, &m.CustomerName); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// MemberRateMinor returns a user's per-project rate, 0 when none is set. It is
// the level between the assignment and the project in the rate resolution order.
func (db *DB) MemberRateMinor(ctx context.Context, userID, projectID int64) (int64, error) {
	var rate int64
	err := db.read.QueryRowContext(ctx,
		`SELECT rate_minor FROM project_members WHERE user_id = ? AND project_id = ?`,
		userID, projectID).Scan(&rate)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return rate, nil
}
