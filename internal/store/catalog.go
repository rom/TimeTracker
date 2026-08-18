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

// ErrNotFound is returned when a row does not exist. The service layer also
// returns it in place of a permission error for resources the caller may not know
// exist, so an unauthorised probe cannot be used to enumerate records.
var ErrNotFound = errors.New("not found")

// --------------------------------------------------------------------- users --

// CreateUser inserts a user and returns it with its assigned id.
func (db *DB) CreateUser(ctx context.Context, u domain.User) (domain.User, error) {
	now := time.Now()
	res, err := db.write.ExecContext(ctx, `
		INSERT INTO users (display_name, email, role, time_zone, theme, language, active, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.DisplayName, u.Email, string(u.Role), u.TimeZone, u.Theme, u.Language,
		boolToInt(u.Active), formatTime(now))
	if err != nil {
		return domain.User{}, fmt.Errorf("create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.User{}, err
	}
	u.ID = id
	u.CreatedAt = now
	return u, nil
}

// GetUser loads one user by id.
func (db *DB) GetUser(ctx context.Context, id int64) (domain.User, error) {
	row := db.read.QueryRowContext(ctx, `
		SELECT id, display_name, email, role, time_zone, theme, language, active, created_at
		FROM users WHERE id = ?`, id)
	return scanUser(row)
}

// FirstUser returns the lowest-numbered user, which in local mode is the only
// one. It is how the single-user identity is resolved at startup.
func (db *DB) FirstUser(ctx context.Context) (domain.User, error) {
	row := db.read.QueryRowContext(ctx, `
		SELECT id, display_name, email, role, time_zone, theme, language, active, created_at
		FROM users ORDER BY id LIMIT 1`)
	return scanUser(row)
}

// UpdateUserPreferences saves the display settings a user can change themselves.
func (db *DB) UpdateUserPreferences(ctx context.Context, id int64, theme, timeZone, language string) error {
	_, err := db.write.ExecContext(ctx,
		`UPDATE users SET theme = ?, time_zone = ?, language = ? WHERE id = ?`,
		theme, timeZone, language, id)
	return err
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, so the per-entity scan
// helpers can serve single-row and multi-row queries alike.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (domain.User, error) {
	var u domain.User
	var role, createdAt string
	var active int
	err := row.Scan(&u.ID, &u.DisplayName, &u.Email, &role, &u.TimeZone, &u.Theme,
		&u.Language, &active, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	u.Role = domain.Role(role)
	u.Active = active != 0
	if u.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.User{}, err
	}
	return u, nil
}

// ----------------------------------------------------------------- customers --

// CreateCustomer inserts a customer.
func (db *DB) CreateCustomer(ctx context.Context, c domain.Customer) (domain.Customer, error) {
	now := time.Now()
	res, err := db.write.ExecContext(ctx, `
		INSERT INTO customers (name, code, currency, colour_key, icon, notes, rate_minor, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, c.Code, c.Currency, c.ColourKey, c.Icon, c.Notes, c.RateMinor, formatTime(now))
	if err != nil {
		return domain.Customer{}, fmt.Errorf("create customer: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.Customer{}, err
	}
	c.ID = id
	c.CreatedAt = now
	return c, nil
}

// UpdateCustomer saves the editable fields of an existing customer.
func (db *DB) UpdateCustomer(ctx context.Context, c domain.Customer) error {
	res, err := db.write.ExecContext(ctx, `
		UPDATE customers SET name = ?, code = ?, currency = ?, colour_key = ?, icon = ?,
		       notes = ?, rate_minor = ?
		WHERE id = ?`,
		c.Name, c.Code, c.Currency, c.ColourKey, c.Icon, c.Notes, c.RateMinor, c.ID)
	if err != nil {
		return fmt.Errorf("update customer: %w", err)
	}
	return requireOneRow(res)
}

// SetCustomerArchived archives or restores a customer. Customers are never
// deleted, because deleting one would orphan invoiced history.
func (db *DB) SetCustomerArchived(ctx context.Context, id int64, archived bool) error {
	var res sql.Result
	var err error
	if archived {
		res, err = db.write.ExecContext(ctx,
			`UPDATE customers SET archived_at = ? WHERE id = ?`, formatTime(time.Now()), id)
	} else {
		res, err = db.write.ExecContext(ctx,
			`UPDATE customers SET archived_at = NULL WHERE id = ?`, id)
	}
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// GetCustomer loads one customer.
func (db *DB) GetCustomer(ctx context.Context, id int64) (domain.Customer, error) {
	row := db.read.QueryRowContext(ctx, `
		SELECT id, name, code, currency, colour_key, icon, notes, rate_minor, archived_at, created_at
		FROM customers WHERE id = ?`, id)
	return scanCustomer(row)
}

// ListCustomers returns customers ordered by name, restricted to the actor's
// scope and optionally including archived ones (needed when reporting over a
// historical period).
func (db *DB) ListCustomers(ctx context.Context, scope Scope, includeArchived bool) ([]domain.Customer, error) {
	var conditions []string
	var args []any
	if !includeArchived {
		conditions = append(conditions, `archived_at IS NULL`)
	}
	// A customer is in scope if the actor is a client of it, or is a member of
	// one of its projects.
	if !scope.Unrestricted {
		switch {
		case scope.CustomerID != 0:
			conditions = append(conditions, `id = ?`)
			args = append(args, scope.CustomerID)
		case len(scope.ProjectIDs) == 0:
			conditions = append(conditions, `1 = 0`)
		default:
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(scope.ProjectIDs)), ",")
			conditions = append(conditions,
				`id IN (SELECT customer_id FROM projects WHERE id IN (`+placeholders+`))`)
			for _, id := range scope.ProjectIDs {
				args = append(args, id)
			}
		}
	}

	query := `
		SELECT id, name, code, currency, colour_key, icon, notes, rate_minor, archived_at, created_at
		FROM customers` + whereClause(conditions) + ` ORDER BY name COLLATE NOCASE`

	rows, err := db.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list customers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var customers []domain.Customer
	for rows.Next() {
		c, err := scanCustomer(rows)
		if err != nil {
			return nil, err
		}
		customers = append(customers, c)
	}
	return customers, rows.Err()
}

func scanCustomer(row rowScanner) (domain.Customer, error) {
	var c domain.Customer
	var archivedAt sql.NullString
	var createdAt string
	err := row.Scan(&c.ID, &c.Name, &c.Code, &c.Currency, &c.ColourKey, &c.Icon,
		&c.Notes, &c.RateMinor, &archivedAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Customer{}, ErrNotFound
	}
	if err != nil {
		return domain.Customer{}, err
	}
	if c.ArchivedAt, err = nullableTime(archivedAt); err != nil {
		return domain.Customer{}, err
	}
	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.Customer{}, err
	}
	return c, nil
}

// ------------------------------------------------------------------ projects --

// CreateProject inserts a project.
func (db *DB) CreateProject(ctx context.Context, p domain.Project) (domain.Project, error) {
	now := time.Now()
	res, err := db.write.ExecContext(ctx, `
		INSERT INTO projects (customer_id, name, code, colour_key, icon, billable_default,
		                      rate_minor, rounding_rule, budget_seconds, budget_minor, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.CustomerID, p.Name, p.Code, p.ColourKey, p.Icon, boolToInt(p.BillableDefault),
		p.RateMinor, p.RoundingRule, p.BudgetSeconds, p.BudgetMinor, formatTime(now))
	if err != nil {
		return domain.Project{}, fmt.Errorf("create project: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.Project{}, err
	}
	p.ID = id
	p.CreatedAt = now
	return p, nil
}

// UpdateProject saves the editable fields of a project.
func (db *DB) UpdateProject(ctx context.Context, p domain.Project) error {
	res, err := db.write.ExecContext(ctx, `
		UPDATE projects SET customer_id = ?, name = ?, code = ?, colour_key = ?, icon = ?,
		       billable_default = ?, rate_minor = ?, rounding_rule = ?,
		       budget_seconds = ?, budget_minor = ?
		WHERE id = ?`,
		p.CustomerID, p.Name, p.Code, p.ColourKey, p.Icon, boolToInt(p.BillableDefault),
		p.RateMinor, p.RoundingRule, p.BudgetSeconds, p.BudgetMinor, p.ID)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	return requireOneRow(res)
}

// SetProjectArchived archives or restores a project.
func (db *DB) SetProjectArchived(ctx context.Context, id int64, archived bool) error {
	var res sql.Result
	var err error
	if archived {
		res, err = db.write.ExecContext(ctx,
			`UPDATE projects SET archived_at = ? WHERE id = ?`, formatTime(time.Now()), id)
	} else {
		res, err = db.write.ExecContext(ctx, `UPDATE projects SET archived_at = NULL WHERE id = ?`, id)
	}
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// projectSelect is shared by the single-row and list queries so the two cannot
// drift in column order, which is the classic way a Scan starts reading the wrong
// field.
const projectSelect = `
	SELECT p.id, p.customer_id, p.name, p.code, p.colour_key, p.icon, p.billable_default,
	       p.rate_minor, p.rounding_rule, p.budget_seconds, p.budget_minor,
	       p.archived_at, p.created_at, c.name, c.currency
	FROM projects p
	JOIN customers c ON c.id = p.customer_id`

// GetProject loads one project with its customer name.
func (db *DB) GetProject(ctx context.Context, id int64) (domain.Project, error) {
	row := db.read.QueryRowContext(ctx, projectSelect+` WHERE p.id = ?`, id)
	return scanProject(row)
}

// ListProjects returns projects within the actor's scope, optionally filtered to
// one customer.
func (db *DB) ListProjects(ctx context.Context, scope Scope, customerID int64, includeArchived bool) ([]domain.Project, error) {
	query := projectSelect
	var args []any
	var conditions []string
	if scoped, scopeArgs := scope.condition("p.id", "p.customer_id"); scoped != "" {
		conditions = append(conditions, scoped)
		args = append(args, scopeArgs...)
	}
	if customerID != 0 {
		conditions = append(conditions, `p.customer_id = ?`)
		args = append(args, customerID)
	}
	if !includeArchived {
		conditions = append(conditions, `p.archived_at IS NULL`)
	}
	query += whereClause(conditions) + ` ORDER BY c.name COLLATE NOCASE, p.name COLLATE NOCASE`

	rows, err := db.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var projects []domain.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func scanProject(row rowScanner) (domain.Project, error) {
	var p domain.Project
	var archivedAt sql.NullString
	var createdAt string
	var billable int
	err := row.Scan(&p.ID, &p.CustomerID, &p.Name, &p.Code, &p.ColourKey, &p.Icon, &billable,
		&p.RateMinor, &p.RoundingRule, &p.BudgetSeconds, &p.BudgetMinor,
		&archivedAt, &createdAt, &p.CustomerName, &p.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	p.BillableDefault = billable != 0
	if p.ArchivedAt, err = nullableTime(archivedAt); err != nil {
		return domain.Project{}, err
	}
	if p.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.Project{}, err
	}
	return p, nil
}

// --------------------------------------------------------------- assignments --

// CreateAssignment inserts an assignment.
func (db *DB) CreateAssignment(ctx context.Context, a domain.Assignment) (domain.Assignment, error) {
	now := time.Now()
	res, err := db.write.ExecContext(ctx, `
		INSERT INTO assignments (project_id, name, code, colour_key, icon, billable_default,
		                         rate_minor, sort_order, favourite, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ProjectID, a.Name, a.Code, a.ColourKey, a.Icon, boolToInt(a.BillableDefault),
		a.RateMinor, a.SortOrder, boolToInt(a.Favourite), formatTime(now))
	if err != nil {
		return domain.Assignment{}, fmt.Errorf("create assignment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.Assignment{}, err
	}
	a.ID = id
	a.CreatedAt = now
	return a, nil
}

// UpdateAssignment saves the editable fields of an assignment.
func (db *DB) UpdateAssignment(ctx context.Context, a domain.Assignment) error {
	res, err := db.write.ExecContext(ctx, `
		UPDATE assignments SET project_id = ?, name = ?, code = ?, colour_key = ?, icon = ?,
		       billable_default = ?, rate_minor = ?, sort_order = ?, favourite = ?
		WHERE id = ?`,
		a.ProjectID, a.Name, a.Code, a.ColourKey, a.Icon, boolToInt(a.BillableDefault),
		a.RateMinor, a.SortOrder, boolToInt(a.Favourite), a.ID)
	if err != nil {
		return fmt.Errorf("update assignment: %w", err)
	}
	return requireOneRow(res)
}

// SetAssignmentArchived archives or restores an assignment.
func (db *DB) SetAssignmentArchived(ctx context.Context, id int64, archived bool) error {
	var res sql.Result
	var err error
	if archived {
		res, err = db.write.ExecContext(ctx,
			`UPDATE assignments SET archived_at = ? WHERE id = ?`, formatTime(time.Now()), id)
	} else {
		res, err = db.write.ExecContext(ctx, `UPDATE assignments SET archived_at = NULL WHERE id = ?`, id)
	}
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// assignmentSelect joins up to the customer so that every assignment carries the
// full path needed for display, pickers and exports without a second query.
const assignmentSelect = `
	SELECT a.id, a.project_id, a.name, a.code, a.colour_key, a.icon, a.billable_default,
	       a.rate_minor, a.sort_order, a.favourite, a.archived_at, a.created_at,
	       p.name, p.customer_id, c.name, c.currency
	FROM assignments a
	JOIN projects  p ON p.id = a.project_id
	JOIN customers c ON c.id = p.customer_id`

// GetAssignment loads one assignment with its project and customer names.
func (db *DB) GetAssignment(ctx context.Context, id int64) (domain.Assignment, error) {
	row := db.read.QueryRowContext(ctx, assignmentSelect+` WHERE a.id = ?`, id)
	return scanAssignment(row)
}

// ListAssignments returns assignments within the actor's scope, optionally
// filtered to one project. Favourites sort first, since the point of marking one
// is to reach it quickly.
func (db *DB) ListAssignments(ctx context.Context, scope Scope, projectID int64, includeArchived bool) ([]domain.Assignment, error) {
	query := assignmentSelect
	var args []any
	var conditions []string
	if scoped, scopeArgs := scope.condition("a.project_id", "p.customer_id"); scoped != "" {
		conditions = append(conditions, scoped)
		args = append(args, scopeArgs...)
	}
	if projectID != 0 {
		conditions = append(conditions, `a.project_id = ?`)
		args = append(args, projectID)
	}
	if !includeArchived {
		conditions = append(conditions, `a.archived_at IS NULL`)
	}
	query += whereClause(conditions) +
		` ORDER BY a.favourite DESC, c.name COLLATE NOCASE, p.name COLLATE NOCASE,
		           a.sort_order, a.name COLLATE NOCASE`

	rows, err := db.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list assignments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var assignments []domain.Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}

func scanAssignment(row rowScanner) (domain.Assignment, error) {
	var a domain.Assignment
	var archivedAt sql.NullString
	var createdAt string
	var billable, favourite int
	err := row.Scan(&a.ID, &a.ProjectID, &a.Name, &a.Code, &a.ColourKey, &a.Icon, &billable,
		&a.RateMinor, &a.SortOrder, &favourite, &archivedAt, &createdAt,
		&a.ProjectName, &a.CustomerID, &a.CustomerName, &a.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Assignment{}, ErrNotFound
	}
	if err != nil {
		return domain.Assignment{}, err
	}
	a.BillableDefault = billable != 0
	a.Favourite = favourite != 0
	if a.ArchivedAt, err = nullableTime(archivedAt); err != nil {
		return domain.Assignment{}, err
	}
	if a.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.Assignment{}, err
	}
	return a, nil
}

// ------------------------------------------------------------------ settings --

// Settings holds the instance-wide defaults from the single settings row.
type Settings struct {
	DefaultCurrency  string
	DefaultRounding  string
	DefaultRateMinor int64
	WeekStart        int
	MaxTimerSeconds  int64
	// Display toggles an administrator sets for the whole instance. A clock in
	// the header is useful to some people and a distraction to others, so it is
	// a choice rather than a decision made for everyone by the designer.
	ShowClock       bool
	ShowTimeAndDate bool
}

// GetSettings reads the single settings row.
func (db *DB) GetSettings(ctx context.Context) (Settings, error) {
	var s Settings
	var showClock, showTimeAndDate int
	err := db.read.QueryRowContext(ctx, `
		SELECT default_currency, default_rounding, default_rate_minor, week_start,
		       max_timer_seconds, show_clock, show_time_and_date
		FROM settings WHERE id = 1`).
		Scan(&s.DefaultCurrency, &s.DefaultRounding, &s.DefaultRateMinor, &s.WeekStart,
			&s.MaxTimerSeconds, &showClock, &showTimeAndDate)
	if err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}
	s.ShowClock = showClock != 0
	s.ShowTimeAndDate = showTimeAndDate != 0
	return s, nil
}

// UpdateSettings saves the instance-wide defaults.
func (db *DB) UpdateSettings(ctx context.Context, s Settings) error {
	_, err := db.write.ExecContext(ctx, `
		UPDATE settings SET default_currency = ?, default_rounding = ?, default_rate_minor = ?,
		       week_start = ?, max_timer_seconds = ?, show_clock = ?, show_time_and_date = ?
		WHERE id = 1`,
		s.DefaultCurrency, s.DefaultRounding, s.DefaultRateMinor, s.WeekStart,
		s.MaxTimerSeconds, boolToInt(s.ShowClock), boolToInt(s.ShowTimeAndDate))
	return err
}

// ------------------------------------------------------------------- helpers --

// boolToInt converts a Go bool to the integer SQLite stores.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// whereClause assembles an optional WHERE from a list of already-parameterised
// conditions. The conditions are always literals from this package; user input
// only ever travels as a bound argument.
func whereClause(conditions []string) string {
	if len(conditions) == 0 {
		return ""
	}
	clause := " WHERE " + conditions[0]
	for _, c := range conditions[1:] {
		clause += " AND " + c
	}
	return clause
}

// requireOneRow turns "the UPDATE matched nothing" into ErrNotFound, so a caller
// updating a record that has been deleted underneath them gets a clear answer
// instead of a silent success.
func requireOneRow(res sql.Result) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}
